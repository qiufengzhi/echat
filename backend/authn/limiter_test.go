// limiter_test.go 登录限流器单元测试：用 miniredis 内存模拟真实 Redis 命令协议
package authn

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedisLimiter 构造接入 miniredis 的 Redis 限流器
// t 测试上下文，用于自动清理与失败断言
func newTestRedisLimiter(t *testing.T) (*redisLoginLimiter, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisLoginLimiter(rdb).(*redisLoginLimiter), srv
}

// TestRedisLimiterLockAfterFiveFailures 连续 5 次失败应触发锁定，且计数键在锁定后清空
func TestRedisLimiterLockAfterFiveFailures(t *testing.T) {
	l, _ := newTestRedisLimiter(t)
	key := "login:test-user"

	// 前 4 次失败只计数不锁定
	for i := 0; i < 4; i++ {
		if l.Failed(key) {
			t.Fatalf("第 %d 次失败不应触发锁定", i+1)
		}
	}
	if l.Locked(key) {
		t.Fatal("未达阈值时不应处于锁定态")
	}

	// 第 5 次失败触发锁定
	if !l.Failed(key) {
		t.Fatal("第 5 次失败应触发锁定")
	}
	if !l.Locked(key) {
		t.Fatal("锁定键应生效")
	}
	if t.Failed() {
		return
	}

	// 锁定期内再次失败仍返回锁定
	if !l.Failed(key) {
		t.Fatal("锁定态内失败应直接返回锁定")
	}
}

// TestRedisLimiterResetUnlocks Reset 应解除锁定并重新计数
func TestRedisLimiterResetUnlocks(t *testing.T) {
	l, _ := newTestRedisLimiter(t)
	key := "login:test-user"

	for i := 0; i < 5; i++ {
		l.Failed(key)
	}
	if !l.Locked(key) {
		t.Fatal("前置条件：应已锁定")
	}

	l.Reset(key)
	if l.Locked(key) {
		t.Fatal("Reset 后应解除锁定")
	}
	// 重置后失败从零计数，第 1 次不应锁定
	if l.Failed(key) {
		t.Fatal("Reset 后首次失败不应触发锁定")
	}
}

// TestRedisLimiterLockWindowExpiry 锁定 TTL 过期后自动解锁，并从零重新计数
func TestRedisLimiterLockWindowExpiry(t *testing.T) {
	l, srv := newTestRedisLimiter(t)
	key := "login:test-user"

	for i := 0; i < 5; i++ {
		l.Failed(key)
	}
	if !l.Locked(key) {
		t.Fatal("前置条件：应已锁定")
	}

	// 推进时间越过锁定 TTL（默认 15 分钟）
	srv.FastForward(16 * time.Minute)
	if l.Locked(key) {
		t.Fatal("锁定 TTL 过期后应自动解锁")
	}
	// 容器 TTL 过期后计数键已被删除，应从零重新计数
	if l.Failed(key) {
		t.Fatal("锁期过后首次失败不应触发锁定")
	}
}