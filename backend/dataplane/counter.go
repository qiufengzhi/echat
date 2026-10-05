package dataplane

import (
	"sync"
	"sync/atomic"
)

// defaultReportEvery 丢弃计数的默认告警间隔：首次与之后每 N 次输出一条
//
// 首次必报保证故障一发生就留痕，其后稀疏上报避免持续拥塞刷爆日志
const defaultReportEvery = 1000

// Counter 丢弃计数器：原子累计 + 限速告警
//
// 供不走 Ring 的丢弃分支使用，保证任何丢弃都不会静默发生
// 零值不可用，必须经 NewCounter 构造
type Counter struct {
	// name 计数器名，出现在告警与运行快照里
	name string
	// every 告警间隔，累计值达到它的整数倍时输出一条
	every int64
	// n 累计丢弃数
	n atomic.Int64
	// unregister 从包级注册表注销自身的函数，不登记时为 nil
	unregister func()
	// closeOnce 保证注销只执行一次
	closeOnce sync.Once
}

// NewCounter 构造丢弃计数器并登记进运行快照
//
// name: 计数器名，建议形如 "tts.stale_segment"
// every: 告警间隔，小于 1 时取默认值 1000
// return: 计数器实例
func NewCounter(name string, every int64) *Counter {
	c := newCounter(name, every)
	c.unregister = registerStat(c)
	return c
}

// newCounter 构造不登记进运行快照的计数器，供 Ring 内部使用
//
// Ring 自己上报丢弃数，内部的计数器不重复出现在快照里
// name: 计数器名
// every: 告警间隔
// return: 计数器实例
func newCounter(name string, every int64) *Counter {
	if every < 1 {
		every = defaultReportEvery
	}
	return &Counter{name: name, every: every}
}

// Inc 自增一次并判断是否需要输出告警
//
// return: 累计丢弃数与是否应记录一条告警，累计值为 1 及其后每隔 every 次时为 true
func (c *Counter) Inc() (total int64, report bool) {
	return c.Add(1)
}

// Add 自增指定数量并判断是否需要输出告警
//
// delta: 本次新增的丢弃数，调用方保证为正数
// return: 累计丢弃数与是否应记录一条告警
func (c *Counter) Add(delta int64) (total int64, report bool) {
	total = c.n.Add(delta)
	return total, total == 1 || total%c.every == 0
}

// Drop 记录一次丢弃，并按限速决定是否输出告警
//
// 调用方不需要自己判断该不该打日志，也就不存在漏打导致静默丢弃的可能
// kv: 附加的结构化日志字段，键值成对出现
// return: 累计丢弃数
func (c *Counter) Drop(kv ...any) int64 {
	total, report := c.Inc()
	if report {
		logger.Warnw("数据丢弃", append(append([]any{}, kv...), "counter", c.name, "droppedTotal", total)...)
	}
	return total
}

// Value 返回累计丢弃数
//
// return: 累计值，0 表示自启动以来未丢弃
func (c *Counter) Value() int64 {
	return c.n.Load()
}

// Name 返回计数器名
//
// return: 构造时传入的名字
func (c *Counter) Name() string {
	return c.name
}

// Close 从运行快照注销本计数器，幂等
//
// 进程级计数器不需要调用；会话级计数器应随会话销毁一并注销
func (c *Counter) Close() {
	c.closeOnce.Do(func() {
		if c.unregister != nil {
			c.unregister()
		}
	})
}

// StatName 实现 Stat，返回计数器名
//
// return: 计数器名
func (c *Counter) StatName() string { return c.name }

// StatKind 实现 Stat，返回结构种类
//
// return: 固定为 counter
func (c *Counter) StatKind() string { return "counter" }

// StatBacklog 实现 Stat，计数器没有积压概念
//
// return: 固定为 0
func (c *Counter) StatBacklog() int { return 0 }

// StatCapacity 实现 Stat，计数器没有容量概念
//
// return: 固定为 0
func (c *Counter) StatCapacity() int { return 0 }

// StatDropped 实现 Stat，返回累计丢弃数
//
// return: 累计丢弃数
func (c *Counter) StatDropped() int64 { return c.Value() }
