// limiter.go 登录失败限流：防爆破，不泄露账户存在性
//
// 蓝图将热状态层定为 Redis；本实现先以进程内限流器落地交点（同接口可整体换 Redis），
// 未来接入 Redis 后改为分布式计数（rlogin:{userID} / rlogin:{ip} 自增 + TTL）
package authn

import (
	"sync"
	"time"
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

// loginLimiterConfig 失败锁定参数，见 spec §4.5
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
