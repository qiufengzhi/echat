package registry

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisKeyPrefix Redis AI 状态存储的 key 前缀
const redisKeyPrefix = "room:ai:"

// redisOpTimeout 单次 Redis 状态操作超时：不阻塞调用方实时路径
const redisOpTimeout = 500 * time.Millisecond

// RedisAIStore AIStateStore 的 Redis 实现：key = room:ai:{code}，值 = 状态串
// 跨进程共享房间 AI 状态：任一实例写入，其余实例可读，为多实例铺路
type RedisAIStore struct {
	// rdb Redis 客户端
	rdb *redis.Client
}

// NewRedisAIStore 构造 Redis AI 状态存储
// rdb Redis 客户端，与登录限流/在线热状态共享连接
func NewRedisAIStore(rdb *redis.Client) *RedisAIStore {
	return &RedisAIStore{rdb: rdb}
}

// aiKey 拼装房间 AI 状态 key
func (s *RedisAIStore) aiKey(roomID string) string {
	return redisKeyPrefix + roomID
}

// Get 实现 AIStateStore：读取状态串，缺失按 offline
func (s *RedisAIStore) Get(roomID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	v, err := s.rdb.Get(ctx, s.aiKey(roomID)).Result()
	if err != nil {
		return "offline"
	}
	return v
}

// Set 实现 AIStateStore：写入状态串（覆盖）
func (s *RedisAIStore) Set(roomID string, state string) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	_ = s.rdb.Set(ctx, s.aiKey(roomID), state, 0).Err()
}

// Remove 实现 AIStateStore：删除状态
func (s *RedisAIStore) Remove(roomID string) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	_ = s.rdb.Del(ctx, s.aiKey(roomID)).Err()
}
