package sfu

import (
	"testing"

	"echat-backend/dataplane"
	asrpb "echat-backend/proto/asr"
)

// newTestDispatcher 构造一个与全局实例隔离的分发器
//
// return: 空的分发器，不注册全局订阅
func newTestDispatcher() *asrDispatcher {
	return &asrDispatcher{
		rooms: make(map[string]*roomSubscriber),
		drop:  dataplane.NewCounter("asr.dispatch.test", 0),
	}
}

// registerRoom 直接登记一个房间订阅者，不起消费协程
//
// 测试要断言队列内容，而消费协程会把内容取走，因此不复用 subscribe
// d: 目标分发器；roomID: 房间短码
// return: 登记好的订阅者
func registerRoom(d *asrDispatcher, roomID string) *roomSubscriber {
	sub := &roomSubscriber{
		roomID: roomID,
		queue:  dataplane.NewRing[*asrpb.TranscriptAudioChunk]("test."+roomID, roomQueueCapacity),
	}
	d.rooms[roomID] = sub
	return sub
}

// TestDispatchRoutesByRoom 识别结果按房间路由到各自订阅者，互不串台
func TestDispatchRoutesByRoom(t *testing.T) {
	d := newTestDispatcher()
	roomA, roomB := registerRoom(d, "room-a"), registerRoom(d, "room-b")
	t.Cleanup(func() {
		roomA.queue.Close()
		roomB.queue.Close()
	})

	d.dispatch(&asrpb.TranscriptAudioChunk{RoomId: "room-a", Text: "a1"})
	d.dispatch(&asrpb.TranscriptAudioChunk{RoomId: "room-a", Text: "a2"})
	d.dispatch(&asrpb.TranscriptAudioChunk{RoomId: "room-b", Text: "b1"})

	for _, want := range []string{"a1", "a2"} {
		got, ok := roomA.queue.Pop()
		if !ok || got.Text != want {
			t.Fatalf("房间 A 期望 %q，得到 %+v（ok=%v）", want, got, ok)
		}
	}
	if _, ok := roomA.queue.Pop(); ok {
		t.Fatal("房间 A 不应收到房间 B 的结果")
	}

	got, ok := roomB.queue.Pop()
	if !ok || got.Text != "b1" {
		t.Fatalf("房间 B 期望 b1，得到 %+v（ok=%v）", got, ok)
	}
}

// TestDispatchDropsWhenRoomGone 房间已销毁时到达的结果被丢弃并计数
func TestDispatchDropsWhenRoomGone(t *testing.T) {
	d := newTestDispatcher()
	registerRoom(d, "room-gone")
	d.unsubscribe("room-gone")

	before := d.drop.Value()
	d.dispatch(&asrpb.TranscriptAudioChunk{RoomId: "room-gone", Text: "late"})

	if got := d.drop.Value() - before; got != 1 {
		t.Fatalf("无处可投的结果应被计数，期望 +1，实际 +%d", got)
	}
}

// TestRoomQueueDropsOldestWhenBacklogged 房间队列积压时挤掉最老的结果
//
// 识别结果是可丢数据：消费端落后时用户要听的是最新那句
func TestRoomQueueDropsOldestWhenBacklogged(t *testing.T) {
	d := newTestDispatcher()
	sub := registerRoom(d, "room-slow")
	t.Cleanup(func() { sub.queue.Close() })

	overflow := 3
	for i := 0; i < roomQueueCapacity+overflow; i++ {
		d.dispatch(&asrpb.TranscriptAudioChunk{RoomId: "room-slow", Seq: int64(i)})
	}

	if got := sub.queue.Len(); got != roomQueueCapacity {
		t.Fatalf("队列应停在容量 %d，实际 %d", roomQueueCapacity, got)
	}
	if got := sub.queue.Dropped(); got != int64(overflow) {
		t.Fatalf("超出容量的 %d 条应被挤掉，实际丢弃 %d", overflow, got)
	}

	head, ok := sub.queue.Pop()
	if !ok || head.Seq != int64(overflow) {
		t.Fatalf("队首应是第 %d 条，得到 %+v", overflow, head)
	}
}

// TestSubscribeIsIdempotentForRoom 同一房间重复订阅返回同一个订阅者
//
// 两个订阅者会让两条消费循环互相抢事件，房间内处理顺序随之失去保证
func TestSubscribeIsIdempotentForRoom(t *testing.T) {
	d := newTestDispatcher()
	first := d.subscribe("room-idem")
	defer d.unsubscribe("room-idem")

	if second := d.subscribe("room-idem"); first != second {
		t.Fatal("同一房间应复用同一个订阅者")
	}
}

// TestUnsubscribeClosesQueue 退订移除订阅者并关闭其队列
func TestUnsubscribeClosesQueue(t *testing.T) {
	d := newTestDispatcher()
	sub := d.subscribe("room-leave")

	d.unsubscribe("room-leave")

	if _, ok := d.rooms["room-leave"]; ok {
		t.Fatal("退订后不应残留订阅者")
	}
	if !sub.queue.Closed() {
		t.Fatal("退订应关闭该房间的结果队列，让消费协程退出")
	}
}
