// limiter.go 登录失败限流：防爆破，不泄露账户存在性
//
// 热状态蓝图定 Redis，默认实现为 Redis 分布式计数（authn:limit: 前缀，跨实例共享锁定态）；
// Redis 完全不可用时 main 侧降级为进程内实现（NewMemLoginLimiter），保证登录链路不因热状态层故障中断
package authn

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// LoginLimiter 登录失败限流抽象：按账户 / 按 IP 两把锁
type LoginLimiter interface {
	// Failed 记一次失败；返回是否已触发锁定（账户 5 次 / IP 频率超标）
	Failed(key string) bool
	// Locked 查询某键当前是否出于锁定态
	Locked(key string) bool
	// Reset 登录成功后清零失败计数
	Reset(key string)
}

// loginLimiterConfig 失败锁定参数
type loginLimiterConfig struct {
	// maxFailures 窗口内最大失败次数，默认 5
	maxFailures int
	// window 统计窗口，默认 15 分钟
	window time.Duration
	// lockTTL 锁定持续时间，默认 15 分钟
	lockTTL time.Duration
}

// defaultLoginLimiterConfig 返回默认参数
func defaultLoginLimiterConfig() loginLimiterConfig {
	return loginLimiterConfig{maxFailures: 5, window: 15 * time.Minute, lockTTL: 15 * time.Minute}
}

// ---------- Redis 分布式实现（默认） ----------

// redisLoginLimiter Redis 实现：计数字段 INCR + 首次 PEXPIRE 固定窗口，计满落锁键并清计数
// 原子性由 Lua 脚本保证，避免「计数-判定-落锁」三步间的并发竞态
type redisLoginLimiter struct {
	rdb  *redis.Client
	conf loginLimiterConfig
}

// authnLimitScript 记账 + 判锁原子脚本
// KEYS[1] 计数键，KEYS[2] 锁键；ARGV[1] 窗口毫秒，ARGV[2] 最大失败数，ARGV[3] 锁定时长毫秒
// 返回 1 表示本次已触发锁定或本就处于锁定态，返回 0 表示失败未超阈
var authnLimitScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
  return 1
end
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if c >= tonumber(ARGV[2]) then
  redis.call('SET', KEYS[2], '1', 'PX', ARGV[3])
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`)

// NewRedisLoginLimiter 构造基于 Redis 的登录限流器
// rdb 已连通且可用的 go-redis 客户端，由调用方负责生命周期
func NewRedisLoginLimiter(rdb *redis.Client) LoginLimiter {
	return &redisLoginLimiter{rdb: rdb, conf: defaultLoginLimiterConfig()}
}

// failKey 组装失败计数键，命名空间 authn:limit: 与后续在线热状态键隔离
func (r *redisLoginLimiter) failKey(key string) string {
	return "authn:limit:fail:" + key
}

// lockKey 组装锁定键，命名空间 authn:limit: 与后续在线热状态键隔离
func (r *redisLoginLimiter) lockKey(key string) string {
	return "authn:limit:lock:" + key
}

// Failed 记一次失败并判锁，见接口注释
// Redis 瞬时不可用时按未锁定处理（fail-open），防锁态读写故障阻断登录主链路
func (r *redisLoginLimiter) Failed(key string) bool {
	locked, err := authnLimitScript.Run(context.Background(), r.rdb,
		[]string{r.failKey(key), r.lockKey(key)},
		r.conf.window.Milliseconds(), r.conf.maxFailures, r.conf.lockTTL.Milliseconds()).Int()
	return err == nil && locked == 1
}

// Locked 查询锁定态，见接口注释；Redis 瞬时不可用时视为未锁定
func (r *redisLoginLimiter) Locked(key string) bool {
	n, err := r.rdb.Exists(context.Background(), r.lockKey(key)).Result()
	return err == nil && n > 0
}

// Reset 清零失败计数并解除锁态，见接口注释
func (r *redisLoginLimiter) Reset(key string) {
	_ = r.rdb.Del(context.Background(), r.failKey(key), r.lockKey(key)).Err()
}

// ---------- 进程内实现（Redis 不可用时的降级） ----------

// memLoginLimiter 进程内实现：map + 互斥锁，单实例部署足用，多副本需换 Redis
type memLoginLimiter struct {
	mu    sync.Mutex
	conf  loginLimiterConfig
	state map[string]*loginMemEntry
}

// loginMemEntry 一个锁定键的运行时状态
type loginMemEntry struct {
	failures   int       // 窗口内失败次数
	firstSeen  time.Time // 窗口起点
	unlockedAt time.Time // 解锁时刻，未锁定为零值
}

// NewMemLoginLimiter 构造进程内限流器
func NewMemLoginLimiter() LoginLimiter {
	return &memLoginLimiter{conf: defaultLoginLimiterConfig(), state: make(map[string]*loginMemEntry)}
}

// Failed 记一次失败，见接口注释
func (m *memLoginLimiter) Failed(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	e, ok := m.state[key]
	if !ok {
		e = &loginMemEntry{}
		m.state[key] = e
	}
	// 已锁定直接返回锁定态
	if now.Before(e.unlockedAt) {
		return true
	}
	// 窗口过期则重置计数
	if now.Sub(e.firstSeen) > m.conf.window {
		e.failures = 0
		e.firstSeen = now
	}
	e.failures++
	if e.failures >= m.conf.maxFailures {
		e.unlockedAt = now.Add(m.conf.lockTTL)
		e.failures = 0 // 锁定期满重新计数
		return true
	}
	return false
}

// Locked 查询锁定态，见接口注释
func (m *memLoginLimiter) Locked(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.state[key]
	if !ok {
		return false
	}
	return time.Now().Before(e.unlockedAt)
}

// Reset 清零失败计数，见接口注释
func (m *memLoginLimiter) Reset(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.state, key)
}