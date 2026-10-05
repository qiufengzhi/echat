// Package dataplane 提供数据面传递机制的公共原语
//
// 解决的问题：项目里每个「队列满了怎么办」的分支各写各的，同一语义出现阻塞、丢弃带告警、
// 丢弃带调试日志、静默丢弃、无界等待五种实现，其中静默丢弃无法归因
//
// 本包按「策略决定结构」提供原语：
//   - Ring：需要淘汰最老元素的有界队列（DropOldest），用定长环形缓冲实现
//   - Counter：不走结构、或结构内没有集中丢弃点的丢弃分支，用独立计数器
//   - 交接语义与 DropNewest 继续用 Go channel 的 select/default，不需要本包的结构
//
// 使用约束：不允许绕开本包直接写无 ctx 的阻塞发送，或用 select/default 静默丢弃
package dataplane

import "echat-backend/logging"

// logger 本包的具名日志器，丢弃告警统一从这里输出
var logger = logging.New("dataplane")

// Outcome 一次入队的结果
type Outcome uint8

// 入队结果取值
const (
	// Accepted 元素已入队，没有丢弃任何元素
	Accepted Outcome = iota
	// DroppedOldest 元素已入队，代价是挤掉了队首最老的一个元素
	DroppedOldest
	// Rejected 元素未入队，队列已关闭
	Rejected
)

// String 返回结果名，用于日志字段，取值 accepted / dropped_oldest / rejected
func (o Outcome) String() string {
	switch o {
	case Accepted:
		return "accepted"
	case DroppedOldest:
		return "dropped_oldest"
	default:
		return "rejected"
	}
}

// Stat 数据面结构的运行快照，由包级注册表统一收集后对外暴露
type Stat interface {
	// StatName 返回结构名，用于定位是哪一个队列
	StatName() string
	// StatKind 返回结构种类，取值 ring / counter
	StatKind() string
	// StatBacklog 返回当前积压元素数，计数器类固定为 0
	StatBacklog() int
	// StatCapacity 返回容量上限，计数器类固定为 0
	StatCapacity() int
	// StatDropped 返回自启动以来的累计丢弃数
	StatDropped() int64
}
