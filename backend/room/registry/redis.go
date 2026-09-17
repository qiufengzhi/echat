package registry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"echat-backend/room/aggregate"
)

// roomRegistryKeyPrefix Redis 房间注册表的 key 前缀
const roomRegistryKeyPrefix = "room:registry:"

// roomSnapshot 房间注册表在 Redis 中的控制面快照
// 只承载低频状态（聚合根 id / 房主 / 角色映射），真实连接对象始终进程本地持有
// 跨节点语义：房间「归属/身份/角色」对集群可见，连接拓扑不落 Redis
type roomSnapshot struct {
	// ID 房间对外短码
	ID string `json:"id"`
	// AggID 房间聚合根 id
	AggID string `json:"agg_id"`
	// HostID 当前房主
	HostID string `json:"host_id"`
	// RoleOf 用户角色映射
	RoleOf map[string]string `json:"role_of,omitempty"`
	// MutedOf 静音叠加集合
	MutedOf map[string]bool `json:"muted_of,omitempty"`
}

// RedisRegistry RoomRegistry 的 Redis 实现：房间控制面状态跨进程共享
// key = room:registry:{code}，值 = roomSnapshot JSON
// 注意：重建的 *aggregate.Room 不含任何真实连接（Clients 为空），只用于跨节点
// 存在性/归属/角色发现；进程内实时房间仍走 MemoryRegistry（默认实现）
type RedisRegistry struct {
	// rdb Redis 客户端
	rdb *redis.Client
}

// NewRedisRegistry 构造 Redis 房间注册表
// rdb Redis 客户端
func NewRedisRegistry(rdb *redis.Client) *RedisRegistry {
	return &RedisRegistry{rdb: rdb}
}

// regKey 拼装房间注册表 key
func (r *RedisRegistry) regKey(roomID string) string {
	return roomRegistryKeyPrefix + roomID
}

// Put 实现 RoomRegistry：序列化房间控制面快照写入 Redis
// room 当前房间聚合；Clients 等连接态不进快照
func (r *RedisRegistry) Put(roomID string, room *aggregate.Room) {
	snap := roomSnapshot{ID: room.ID, AggID: room.AggID, HostID: room.HostID}
	room.Lock.RLock()
	if room.RoleOf != nil {
		snap.RoleOf = make(map[string]string, len(room.RoleOf))
		for k, v := range room.RoleOf {
			snap.RoleOf[k] = v
		}
	}
	if room.MutedOf != nil {
		snap.MutedOf = make(map[string]bool, len(room.MutedOf))
		for k, v := range room.MutedOf {
			snap.MutedOf[k] = v
		}
	}
	room.Lock.RUnlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	_ = r.rdb.Set(ctx, r.regKey(roomID), data, 0).Err()
}

// Get 实现 RoomRegistry：从 Redis 反序列化房间控制面快照
// 返回的房间无在线连接，仅供存在性/归属/角色查询
func (r *RedisRegistry) Get(roomID string) (*aggregate.Room, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	data, err := r.rdb.Get(ctx, r.regKey(roomID)).Bytes()
	if err != nil {
		return nil, false
	}
	var snap roomSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, false
	}
	room := aggregate.NewRoom(snap.ID)
	room.AggID = snap.AggID
	room.HostID = snap.HostID
	for k, v := range snap.RoleOf {
		room.RoleOf[k] = v
	}
	for k, v := range snap.MutedOf {
		room.MutedOf[k] = v
	}
	return room, true
}

// Delete 实现 RoomRegistry
func (r *RedisRegistry) Delete(roomID string) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	_ = r.rdb.Del(ctx, r.regKey(roomID)).Err()
}

// Exists 实现 RoomRegistry
func (r *RedisRegistry) Exists(roomID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	n, err := r.rdb.Exists(ctx, r.regKey(roomID)).Result()
	return err == nil && n > 0
}

// All 实现 RoomRegistry：扫描 room:registry:* 前缀并逐个读回
func (r *RedisRegistry) All() map[string]*aggregate.Room {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out := make(map[string]*aggregate.Room)
	iter := r.rdb.Scan(ctx, 0, roomRegistryKeyPrefix+"*", 50).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		if room, ok := r.Get(key[len(roomRegistryKeyPrefix):]); ok {
			out[room.ID] = room
		}
	}
	return out
}
