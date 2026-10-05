package dataplane

import (
	"sync"
	"testing"
	"time"
)

// TestRingFIFOOrder 未满时按入队顺序取出
func TestRingFIFOOrder(t *testing.T) {
	r := NewRing[int]("test.fifo", 3)
	defer r.Close()

	for i := 1; i <= 3; i++ {
		if got := r.Push(i); got != Accepted {
			t.Fatalf("第 %d 次 Push 期望 Accepted，得到 %s", i, got)
		}
	}
	if r.Len() != 3 {
		t.Fatalf("期望积压 3，得到 %d", r.Len())
	}
	if r.Dropped() != 0 {
		t.Fatalf("未满时不应有丢弃，得到 %d", r.Dropped())
	}

	for want := 1; want <= 3; want++ {
		got, ok := r.Pop()
		if !ok || got != want {
			t.Fatalf("第 %d 次 Pop 期望 %d，得到 %d（ok=%v）", want, want, got, ok)
		}
	}
	if r.Len() != 0 {
		t.Fatalf("排空后期望积压 0，得到 %d", r.Len())
	}
}

// TestRingDropOldestWhenFull 满时挤掉最老元素，留下的是最新的元素
func TestRingDropOldestWhenFull(t *testing.T) {
	r := NewRing[int]("test.drop", 3)
	defer r.Close()

	for i := 1; i <= 5; i++ {
		r.Push(i)
	}

	if r.Len() != 3 {
		t.Fatalf("期望积压停在容量 3，得到 %d", r.Len())
	}
	if r.Dropped() != 2 {
		t.Fatalf("期望丢弃 2（元素 1 与 2），得到 %d", r.Dropped())
	}

	for _, want := range []int{3, 4, 5} {
		got, ok := r.Pop()
		if !ok || got != want {
			t.Fatalf("期望取到 %d，得到 %d（ok=%v）", want, got, ok)
		}
	}
}

// TestRingPushDropsAtMostOne 连续驱逐时每次 Push 至多丢弃一个元素
func TestRingPushDropsAtMostOne(t *testing.T) {
	const (
		capacity = 2
		pushes   = 10
	)
	r := NewRing[int]("test.evict", capacity)
	defer r.Close()

	for i := 1; i <= pushes; i++ {
		before := r.Dropped()
		if got := r.Push(i); got != Accepted && got != DroppedOldest {
			t.Fatalf("第 %d 次 Push 期望入队成功，得到 %s", i, got)
		}
		if delta := r.Dropped() - before; delta > 1 {
			t.Fatalf("第 %d 次 Push 丢弃了 %d 个元素，超过每次至多一个", i, delta)
		}
		if r.Len() > capacity {
			t.Fatalf("第 %d 次 Push 后积压 %d 超过容量 %d", i, r.Len(), capacity)
		}
	}

	if want := int64(pushes - capacity); r.Dropped() != want {
		t.Fatalf("期望累计丢弃 %d，得到 %d", want, r.Dropped())
	}

	got, _ := r.Pop()
	if want := pushes - capacity + 1; got != want {
		t.Fatalf("期望队首是最新的第 %d 个元素，得到 %d", want, got)
	}
}

// TestRingPopOnEmpty 空队列 Pop 返回 false 而不是阻塞
func TestRingPopOnEmpty(t *testing.T) {
	r := NewRing[string]("test.empty", 2)
	defer r.Close()

	if _, ok := r.Pop(); ok {
		t.Fatal("空队列 Pop 应返回 false")
	}
}

// TestRingPushZeroCapacity 容量非法时按 1 处理
func TestRingPushZeroCapacity(t *testing.T) {
	r := NewRing[int]("test.zerocap", 0)
	defer r.Close()

	if r.Cap() != 1 {
		t.Fatalf("容量 0 期望被抬到 1，得到 %d", r.Cap())
	}
	r.Push(1)
	r.Push(2)
	if r.Len() != 1 || r.Dropped() != 1 {
		t.Fatalf("容量 1 下推两个元素，期望积压 1 丢弃 1，得到积压 %d 丢弃 %d", r.Len(), r.Dropped())
	}
}

// TestRingCloseDrainsRemaining 关闭后仍可取走剩余元素，排空后 Pop 返回 false
func TestRingCloseDrainsRemaining(t *testing.T) {
	r := NewRing[int]("test.drain", 3)
	r.Push(1)
	r.Push(2)
	r.Close()

	for _, want := range []int{1, 2} {
		got, ok := r.Pop()
		if !ok || got != want {
			t.Fatalf("关闭后仍应能取到 %d，得到 %d（ok=%v）", want, got, ok)
		}
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("排空后 Pop 应返回 false")
	}
	if !r.Closed() {
		t.Fatal("关闭后 Closed 应为 true")
	}
}

// TestRingCloseRejectsFurtherPush 关闭后 Push 返回 Rejected 且不计入丢弃
func TestRingCloseRejectsFurtherPush(t *testing.T) {
	r := NewRing[int]("test.reject", 2)
	r.Close()

	if got := r.Push(1); got != Rejected {
		t.Fatalf("关闭后 Push 期望 Rejected，得到 %s", got)
	}
	if r.Dropped() != 0 {
		t.Fatalf("拒绝入队不是丢弃，期望丢弃 0，得到 %d", r.Dropped())
	}
}

// TestRingCloseIdempotent 重复关闭不 panic、不重复注销
func TestRingCloseIdempotent(t *testing.T) {
	r := NewRing[int]("test.idem", 2)
	r.Close()
	r.Close()
	r.Close()
}

// TestRingWaitWakesOnPush 有元素时 Wait 立即可读
func TestRingWaitWakesOnPush(t *testing.T) {
	r := NewRing[int]("test.wait", 2)
	defer r.Close()

	select {
	case <-r.Wait():
		t.Fatal("刚构造且为空的队列不应有通知")
	default:
	}

	r.Push(1)
	select {
	case <-r.Wait():
	case <-time.After(time.Second):
		t.Fatal("Push 后应立即可等")
	}
}

// TestRingWaitUnblocksOnClose 关闭时唤醒等待者，避免消费者永久阻塞
func TestRingWaitUnblocksOnClose(t *testing.T) {
	r := NewRing[int]("test.waitclose", 2)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := r.Pop(); ok {
				continue
			}
			if r.Closed() {
				return
			}
			select {
			case <-r.Wait():
			case <-time.After(2 * time.Second):
				t.Error("等待超时：关闭没有唤醒消费者")
				return
			}
		}
	}()

	r.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("消费者未在关闭后退出")
	}
}

// TestRingCloseDeregistersFromSnapshot 关闭后该队列从运行快照注销
func TestRingCloseDeregistersFromSnapshot(t *testing.T) {
	const name = "test.snapshot.deregister"

	r := NewRing[int](name, 4)
	r.Push(1)
	r.Push(2)

	entry, ok := findSnapshot(name)
	if !ok {
		t.Fatal("登记后的队列应出现在快照里")
	}
	if entry.Kind != "ring" || entry.Backlog != 2 || entry.Capacity != 4 {
		t.Fatalf("快照字段不符：%+v", entry)
	}

	r.Close()
	if _, ok := findSnapshot(name); ok {
		t.Fatal("关闭后的队列不应再出现在快照里")
	}
}

// TestRingDroppedAppearsInSnapshot 丢弃数出现在快照里，供无告警时查看现状
func TestRingDroppedAppearsInSnapshot(t *testing.T) {
	const name = "test.snapshot.dropped"

	r := NewRing[int](name, 1)
	defer r.Close()
	r.Push(1)
	r.Push(2)
	r.Push(3)

	entry, ok := findSnapshot(name)
	if !ok {
		t.Fatal("队列应出现在快照里")
	}
	if entry.Dropped != 2 {
		t.Fatalf("期望快照丢弃数为 2，得到 %d", entry.Dropped)
	}
}

// TestRingConcurrentProducerConsumer 并发下不变量成立且元素有序
//
// 不变量：入队成功的元素总数 = 取出的 + 被挤掉的，即丢弃之外的元素一个都不会凭空消失
func TestRingConcurrentProducerConsumer(t *testing.T) {
	const (
		capacity = 4
		pushes   = 2000
	)

	r := NewRing[int]("test.concurrent", capacity)

	var accepted int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= pushes; i++ {
			if r.Push(i) != Rejected {
				accepted++
			}
		}
		r.Close()
	}()

	var popped int64
	last := 0
	for {
		if v, ok := r.Pop(); ok {
			if v <= last {
				t.Fatalf("取出顺序被破坏：%d 出现在 %d 之后", v, last)
			}
			last = v
			popped++
			continue
		}
		if r.Closed() {
			break
		}
		select {
		case <-r.Wait():
		case <-time.After(3 * time.Second):
			t.Fatal("等待超时")
		}
	}
	wg.Wait()

	if accepted != int64(pushes) {
		t.Fatalf("生产者未全部入队：期望 %d，得到 %d", pushes, accepted)
	}
	if accepted != popped+r.Dropped() {
		t.Fatalf("元素守恒被破坏：入队 %d，取出 %d，丢弃 %d", accepted, popped, r.Dropped())
	}
}
