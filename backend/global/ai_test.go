package global

import (
	"testing"
	"time"
)

// newTestStore 构造一个与全局实例隔离的状态存储
//
// return: 空的状态存储，不注册全局订阅
func newTestStore() *aiStore {
	return &aiStore{
		rooms: make(map[string]*roomAI),
		subs:  make(map[string]*AIStateSub),
	}
}

// TestSubscriberReceivesTransition 订阅后能取到状态迁移
func TestSubscriberReceivesTransition(t *testing.T) {
	store := newTestStore()
	const room = "r-receive"
	sub := store.Subscribe(room)

	store.SetOnline(room)

	evt, ok := sub.Next()
	if !ok {
		t.Fatal("订阅后应能取到状态迁移")
	}
	if evt.RoomID != room || evt.State != AIOnline {
		t.Fatalf("事件不符：%+v", evt)
	}
	if _, ok := sub.Next(); ok {
		t.Fatal("取走后不应再重复给出同一条迁移")
	}
}

// TestIntermediateTransitionsAreCoalesced 未被取走的中间迁移被合并，只留最终状态
//
// 这条是 O3 的核心：状态满时的行为是合并而不是丢弃，前端不会停在错误的中间值上
func TestIntermediateTransitionsAreCoalesced(t *testing.T) {
	store := newTestStore()
	const room = "r-coalesce"
	sub := store.Subscribe(room)

	store.SetOnline(room)
	store.SetOffline(room)
	store.SetOnline(room)

	evt, ok := sub.Next()
	if !ok {
		t.Fatal("应能取到状态")
	}
	if evt.State != AIOnline {
		t.Fatalf("应取到最终状态 online，得到 %s", evt.State)
	}
	if _, ok := sub.Next(); ok {
		t.Fatal("中间迁移应已被合并，不应再给出")
	}
	if got := sub.Coalesced(); got != 2 {
		t.Fatalf("期望合并 2 次，实际 %d", got)
	}
}

// TestNoSubscriberDoesNotBlock 房间没有订阅者时状态迁移照常完成
func TestNoSubscriberDoesNotBlock(t *testing.T) {
	store := newTestStore()
	const room = "r-nobody"

	store.SetOnline(room)
	if got := store.Get(room); got != AIOnline {
		t.Fatalf("无订阅者时状态仍应写入，得到 %s", got)
	}
}

// TestRemoveClosesSubscription 房间销毁后订阅被关闭、订阅者数归零
func TestRemoveClosesSubscription(t *testing.T) {
	store := newTestStore()
	const room = "r-remove"
	sub := store.Subscribe(room)

	store.Remove(room)

	if _, ok := store.subs[room]; ok {
		t.Fatal("销毁房间后不应残留订阅者")
	}
	if !sub.Closed() {
		t.Fatal("销毁房间应关闭其订阅")
	}
	select {
	case <-sub.Wait():
	default:
		t.Fatal("关闭订阅应唤醒等待者")
	}
}

// TestSubscribeIsIdempotent 同一房间重复订阅返回同一个订阅者
//
// 状态是房间级的，给出两个订阅者只会让两条消费循环互相抢事件
func TestSubscribeIsIdempotent(t *testing.T) {
	store := newTestStore()
	const room = "r-idem"

	if first, second := store.Subscribe(room), store.Subscribe(room); first != second {
		t.Fatal("同一房间应复用同一个订阅者")
	}
}

// TestConcurrentTransitionsEndAtLatestState 并发迁移下订阅者最终读到的是最后写入的状态
func TestConcurrentTransitionsEndAtLatestState(t *testing.T) {
	store := newTestStore()
	const room = "r-concurrent"
	sub := store.Subscribe(room)

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < 200; i++ {
			store.SetOnline(room)
			store.SetOffline(room)
		}
	}()

	var (
		last    AIState
		started bool
	)
	for {
		if evt, ok := sub.Next(); ok {
			last, started = evt.State, true
			continue
		}

		select {
		case <-writerDone:
			// 写入已结束，排空后最后一次读到的必须是最终状态
			for {
				evt, ok := sub.Next()
				if !ok {
					break
				}
				last, started = evt.State, true
			}
			if !started {
				t.Fatal("整个过程未取到任何状态")
			}
			if last != AIOffline {
				t.Fatalf("最终应为 offline，得到 %s", last)
			}
			return
		default:
		}

		select {
		case <-sub.Wait():
		case <-time.After(2 * time.Second):
			t.Fatal("等待状态超时")
		}
	}
}
