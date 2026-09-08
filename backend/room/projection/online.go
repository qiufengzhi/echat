// online.go Redis 在线热状态投影：事实/瞬态分层的瞬态层
//
// 分层：事实层 = Postgres rooms / room_members（谁来过、历史在场，权威落库，见 room/persist）
// 瞬态层 = Redis 房间在线 ZSET（现在谁在线，毫秒级心跳，不落事实）
// 结构：room:online:{roomID} ZSET，member = userID，score = 最近心跳 Unix 秒
//   - join/心跳刷新 score 并续 key 级 TTL（兜底防挂机残留）
//   - leave/断线 ZREM 移除；集合清空后 key 自动过期删除（Redis 键生命周期完整）
//   - 定期 sweep 清走超 5 分钟无心跳的成员，避免僵尸成员占用席位
// Redis 不可用时心跳静默跳过，在线热状态降级为空集合，不影响实时信令主链路
package projection

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// onlineRdb Redis 客户端引用，由编排层注入（与登录限流共享同一连接）
var onlineRdb *redis.Client

// onlineTTL 房间在线集合在无心跳后的存活时长：到期视为全员离线，key 清理
const onlineTTL = 5 * time.Minute

// onlineSweepInterval 过期成员扫刷间隔：控制台观察键生命周期足够敏锐
const onlineSweepInterval = 30 * time.Second

// onlineTimeout 单次 Redis 在线状态操作超时：不阻塞实时信令路径
const onlineTimeout = 500 * time.Millisecond

// onlineKeyPrefix 房间在线集合的 key 前缀
const onlineKeyPrefix = "room:online:"

// SetOnlineStore 注入 Redis 客户端，启用在线热状态层；nil 时心跳静默跳过
func SetOnlineStore(rdb *redis.Client) {
	onlineRdb = rdb
}

// onlineKey 拼装房间在线集合 key（短码做键，redis-cli 观察直观且进程重启键不变）
func onlineKey(roomID string) string {
	return onlineKeyPrefix + roomID
}

// MarkOnline 记录用户在线心跳：ZADD 刷新 score 为当前时刻并续 key 级 TTL
// roomID 房间短码，userID 鉴权用户身份
func MarkOnline(roomID, userID string) {
	if onlineRdb == nil || roomID == "" || userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), onlineTimeout)
	defer cancel()
	pipe := onlineRdb.Pipeline()
	pipe.ZAdd(ctx, onlineKey(roomID), redis.Z{Score: float64(time.Now().Unix()), Member: userID})
	pipe.Expire(ctx, onlineKey(roomID), onlineTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		logger.Warnw("在线心跳写入失败", "roomID", roomID, "error", err)
	}
}

// MarkOffline 用户离开/断线时从房间在线集合移除
func MarkOffline(roomID, userID string) {
	if onlineRdb == nil || roomID == "" || userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), onlineTimeout)
	defer cancel()
	if err := onlineRdb.ZRem(ctx, onlineKey(roomID), userID).Err(); err != nil {
		logger.Warnw("在线成员移除失败", "roomID", roomID, "userID", userID, "error", err)
	}
}

// OnlineMembers 返回房间当前在线成员用户 ID 列表（按最近心跳从新到旧）
// Redis 不可用或房间无在线成员时返回空列表
func OnlineMembers(roomID string) []string {
	if onlineRdb == nil || roomID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), onlineTimeout)
	defer cancel()
	members, err := onlineRdb.ZRevRange(ctx, onlineKey(roomID), 0, -1).Result()
	if err != nil {
		logger.Warnw("在线成员查询失败", "roomID", roomID, "error", err)
		return nil
	}
	return members
}

// IsUserOnline 判定用户当前是否在指定房间在线集合中
func IsUserOnline(roomID, userID string) bool {
	if onlineRdb == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), onlineTimeout)
	defer cancel()
	n, err := onlineRdb.ZScore(ctx, onlineKey(roomID), userID).Result()
	if err != nil {
		return false
	}
	return time.Since(time.Unix(int64(n), 0)) <= onlineTTL
}

// StartOnlineSweep 启动在线热状态扫刷协程，兜底收敛超时无心跳的 Redis 在线成员
func StartOnlineSweep() {
	go sweepOnlineLoop()
}

// sweepOnlineLoop 定期扫刷所有房间在线集合：清走超时成员并回收空 key
func sweepOnlineLoop() {
	ticker := time.NewTicker(onlineSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		sweepOnlineRooms()
	}
}

// sweepOnlineRooms 遍历 Redis 中全部 room:online:* 键，删除超时成员，集合清空后删 key
// 基于 Redis 扫描而非内存房间表：进程重启后残留的 key 也能被收敛，防僵尸在线
func sweepOnlineRooms() {
	if onlineRdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cutoff := strconv.FormatInt(time.Now().Add(-onlineTTL).Unix(), 10)
	iter := onlineRdb.Scan(ctx, 0, onlineKeyPrefix+"*", 50).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		if err := onlineRdb.ZRemRangeByScore(ctx, key, "-inf", cutoff).Err(); err != nil {
			logger.Warnw("在线集合超时成员清理失败", "key", key, "error", err)
			continue
		}
		n, err := onlineRdb.ZCard(ctx, key).Result()
		if err == nil && n == 0 {
			_ = onlineRdb.Del(ctx, key) // 空集合即刻回收，键生命周期完整收敛
		}
	}
}
