package dataplane

import "sync"

// Ring 定长环形缓冲，满时挤掉最老元素
//
// 用于「最新数据最有价值」的连续流：音频帧、识别结果、回复片段
// 单生产者单消费者使用。消费者每次先 Pop，取空后再等 Wait，唤醒后有信号缓冲因此不会丢
// 零值不可用，必须经 NewRing 构造
type Ring[T any] struct {
	// name 队列名，出现在丢弃告警与运行快照里，用于定位
	name string
	// buf 定长存储，长度即队列容量
	buf []T
	// head 下一个待读出元素的下标
	head int
	// size 当前元素个数，读写都在 mu 保护下
	size int
	// mu 保护 buf、head、size 与 closed
	mu sync.Mutex
	// notify 容量 1 的通知信号，满时投递被丢弃；Close 时关闭它来唤醒等待者
	notify chan struct{}
	// drop 丢弃计数与限速告警
	drop *Counter
	// closed 是否已关闭，关闭后 Push 被拒绝、Pop 排空后返回 false
	closed bool
	// unregister 从包级注册表注销自身的函数
	unregister func()
}

// NewRing 构造定长环形缓冲并登记进运行快照
//
// name: 队列名，建议形如 "asr.input"
// capacity: 容量，小于 1 时按 1 处理
// return: 环形缓冲实例
func NewRing[T any](name string, capacity int) *Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	r := &Ring[T]{
		name:   name,
		buf:    make([]T, capacity),
		notify: make(chan struct{}, 1),
		drop:   newCounter("ring."+name, defaultReportEvery),
	}
	r.unregister = registerStat(r)
	return r
}

// Push 入队一个元素，永不阻塞
//
// 队列未满时直接入队；已满时挤掉队首最老元素再写入，并计入一次丢弃
// 一次 Push 至多丢弃一个元素，因此该函数执行后队列长度绝不会超过容量
// v: 待入队元素
// return: 入队结果，队列已关闭时返回 Rejected 且不丢弃任何元素
func (r *Ring[T]) Push(v T) Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return Rejected
	}

	outcome := Accepted
	if r.size == len(r.buf) {
		// 满：head 既是队首也是下一个写入位，就地覆盖即完成淘汰
		r.buf[r.head] = v
		r.head = (r.head + 1) % len(r.buf)
		outcome = DroppedOldest
		r.drop.Drop("queue", r.name)
	} else {
		r.buf[(r.head+r.size)%len(r.buf)] = v
		r.size++
	}

	// 在锁内投递通知：与 Close 关闭 notify 互斥，避免向已关闭通道发送
	r.signal()
	return outcome
}

// Pop 取出队首最老的元素，永不阻塞
//
// return: 元素与是否取到；队列为空时返回零值与 false，此时应看 Closed 判断是否还会再有元素
func (r *Ring[T]) Pop() (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size == 0 {
		var zero T
		return zero, false
	}

	v := r.buf[r.head]
	var zero T
	r.buf[r.head] = zero // 置零，避免已取走的元素仍被底层数组引用而无法回收
	r.head = (r.head + 1) % len(r.buf)
	r.size--
	return v, true
}

// Wait 返回等待通知的通道，队列有新元素或已关闭时被唤醒
//
// 通道容量 1 且 Close 时被关闭，因此不会丢唤醒。调用方每次被唤醒后应重新 Pop，
// 并用 Closed 判断是继续等待还是退出，否则队列关闭后会空转
// return: 只读通知通道
func (r *Ring[T]) Wait() <-chan struct{} {
	return r.notify
}

// signal 非阻塞地投递一次通知
//
// 只在持锁时调用，此时 closed 必然为假，因此不会向已关闭的 notify 发送
func (r *Ring[T]) signal() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// Len 返回当前积压元素数
//
// return: 元素个数，与 Push、Pop 一样在锁内读取，返回的是真实存在过的状态
func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Cap 返回容量上限
//
// return: 容量，构造后不再变化
func (r *Ring[T]) Cap() int {
	return len(r.buf)
}

// Dropped 返回累计丢弃元素数
//
// return: 被挤掉的元素总数，0 表示自启动以来未丢弃
func (r *Ring[T]) Dropped() int64 {
	return r.drop.Value()
}

// Name 返回队列名
//
// return: 构造时传入的名字
func (r *Ring[T]) Name() string {
	return r.name
}

// Closed 返回队列是否已关闭
//
// return: 已关闭返回 true
func (r *Ring[T]) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Close 关闭队列并唤醒等待者，幂等
//
// 关闭后 Push 一律返回 Rejected，Pop 仍可取出剩余元素，排空后返回 false
// 同时从运行快照注销，会话级队列必须在会话销毁时调用，否则快照会残留条目
func (r *Ring[T]) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.notify)
	r.mu.Unlock()

	if r.unregister != nil {
		r.unregister()
		r.unregister = nil
	}
}

// StatName 实现 Stat，返回队列名
//
// return: 队列名
func (r *Ring[T]) StatName() string { return r.name }

// StatKind 实现 Stat，返回结构种类
//
// return: 固定为 ring
func (r *Ring[T]) StatKind() string { return "ring" }

// StatBacklog 实现 Stat，返回当前积压元素数
//
// return: 元素个数
func (r *Ring[T]) StatBacklog() int { return r.Len() }

// StatCapacity 实现 Stat，返回容量上限
//
// return: 容量
func (r *Ring[T]) StatCapacity() int { return r.Cap() }

// StatDropped 实现 Stat，返回累计丢弃数
//
// return: 累计被挤掉的元素数
func (r *Ring[T]) StatDropped() int64 { return r.Dropped() }
