// agentcli_test.go Agent 客户端单元测试：分片取模、在途轮次表、真实 gRPC 服务端上的发起与取消
package agentcli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llmpb "echat-backend/proto/llm"

	"google.golang.org/grpc"
)

// fakeAgent 测试用 Agent 服务端：逐事件产出，可观察取消与释放
type fakeAgent struct {
	llmpb.UnimplementedLLMServiceServer
	events    int           // 每轮流式发送的事件数（不含结束事件）
	interval  time.Duration // 事件间隔，0 表示立即发完
	started   chan string   // 每轮开始时投递 turnID，用于同步测试
	cancelled int32         // 被取消的轮次数
	mu        sync.Mutex    // 保护 closed
	closed    []string      // CloseSession 收到的会话 ID
}

// Generate 逐事件产出，收到取消时立刻返回
//
// req: 轮次请求
// stream: 服务端响应流
// return: 流上下文结束或发送失败时返回错误
func (f *fakeAgent) Generate(req *llmpb.TurnRequest, stream llmpb.LLMService_GenerateServer) error {
	if f.started != nil {
		select {
		case f.started <- req.TurnId:
		default:
		}
	}
	for i := 0; i < f.events; i++ {
		if err := stream.Send(&llmpb.TurnResponse{
			SessionId:    req.SessionId,
			TurnId:       req.TurnId,
			ResponseText: fmt.Sprintf("t%d", i),
		}); err != nil {
			return err
		}
		if f.interval > 0 {
			select {
			case <-stream.Context().Done():
				atomic.AddInt32(&f.cancelled, 1)
				return stream.Context().Err()
			case <-time.After(f.interval):
			}
		}
	}
	return stream.Send(&llmpb.TurnResponse{SessionId: req.SessionId, TurnId: req.TurnId, IsFinal: true})
}

// CloseSession 记录释放请求并返回已释放
//
// ctx: 调用上下文，测试中未使用
// req: 释放请求
// return: closed 恒为 true，无错误
func (f *fakeAgent) CloseSession(_ context.Context, req *llmpb.CloseSessionRequest) (*llmpb.CloseSessionResponse, error) {
	f.mu.Lock()
	f.closed = append(f.closed, req.SessionId)
	f.mu.Unlock()
	return &llmpb.CloseSessionResponse{Closed: true}, nil
}

// closedSessions 取已收到的释放会话列表快照
//
// return: 会话 ID 列表副本
func (f *fakeAgent) closedSessions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

// startFakeAgent 起一个真实 gRPC 服务端并返回地址
//
// t: 测试上下文，用于注册清理
// f: 假服务端实现
// return: 监听地址
func startFakeAgent(t *testing.T, f *fakeAgent) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := grpc.NewServer()
	llmpb.RegisterLLMServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// newTestClient 构造指向假服务端的客户端
//
// t: 测试上下文，用于注册清理
// addr: 服务端地址
// dispatch: 响应回调，可为 nil
// return: 客户端实例
func newTestClient(t *testing.T, addr string, dispatch func(*llmpb.TurnResponse)) *Client {
	t.Helper()
	c, err := New(Options{Addr: addr, Shards: 2, TurnTimeout: 5 * time.Second, Dispatch: dispatch})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// waitFor 轮询等待条件成立，超时则判定测试失败
//
// t: 测试上下文
// timeout: 最长等待时间
// cond: 条件函数
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestShardIndexStableAndBounded 分片下标应稳定且落在范围内
func TestShardIndexStableAndBounded(t *testing.T) {
	for _, shards := range []int{1, 4, 8} {
		first := shardIndex("client1-room1", shards)
		if first < 0 || first >= shards {
			t.Fatalf("分片下标越界: %d (shards=%d)", first, shards)
		}
		if again := shardIndex("client1-room1", shards); again != first {
			t.Fatalf("同一会话应稳定落片: %d != %d", again, first)
		}
	}
}

// TestRegistryRejectsConcurrentTurnAndEndIsScoped 同会话不并发；非本轮的 End 不清除登记
func TestRegistryRejectsConcurrentTurnAndEndIsScoped(t *testing.T) {
	r := NewRegistry()
	var cancelCount int32
	if err := r.Begin("s1", "t1", func() { atomic.AddInt32(&cancelCount, 1) }); err != nil {
		t.Fatalf("首次登记应成功: %v", err)
	}
	if err := r.Begin("s1", "t2", func() {}); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("同会话第二轮应被拒绝，实际: %v", err)
	}
	// 迟到的旧轮次结束不应清掉新轮次的登记
	r.End("s1", "t2")
	if _, ok := r.TurnID("s1"); !ok {
		t.Fatal("非本轮的 End 不应清除登记")
	}
	if !r.Cancel("s1") {
		t.Fatal("首次取消应返回 true")
	}
	if r.Cancel("s1") {
		t.Fatal("同一轮重复取消应返回 false")
	}
	if got := atomic.LoadInt32(&cancelCount); got != 1 {
		t.Fatalf("取消函数应只被调用一次，实际 %d 次", got)
	}
	r.End("s1", "t1")
	if r.Inflight() != 0 {
		t.Fatalf("轮次结束后在途数应为 0，实际 %d", r.Inflight())
	}
}

// TestSendTurnDispatchesAllResponses 一轮的全部事件都应分发给回调，并计入统计
func TestSendTurnDispatchesAllResponses(t *testing.T) {
	fake := &fakeAgent{events: 3}
	addr := startFakeAgent(t, fake)
	got := make(chan *llmpb.TurnResponse, 16)
	c := newTestClient(t, addr, func(r *llmpb.TurnResponse) { got <- r })

	if err := c.SendTurn(&llmpb.TurnRequest{SessionId: "s1", RoomId: "r1", TurnId: "t1", UserText: "嗨"}); err != nil {
		t.Fatalf("发起轮次失败: %v", err)
	}
	for i := 0; i < 4; i++ { // 3 个 token 事件 + 1 个结束事件
		select {
		case resp := <-got:
			if resp.TurnId != "t1" {
				t.Fatalf("第 %d 个事件 turnID 不符: %q", i+1, resp.TurnId)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 个事件超时", i+1)
		}
	}
	waitFor(t, 2*time.Second, func() bool { return c.Inflight() == 0 })
	st := c.Stats()
	if st.Sent != 1 || st.Responses != 4 || st.Failed != 0 || st.Cancelled != 0 {
		t.Fatalf("计数不符: %+v", st)
	}
}

// TestSendTurnRejectsSecondTurnSameSession 会话内已有在途轮次时拒绝新轮次并计数
func TestSendTurnRejectsSecondTurnSameSession(t *testing.T) {
	fake := &fakeAgent{events: 50, interval: 20 * time.Millisecond, started: make(chan string, 4)}
	addr := startFakeAgent(t, fake)
	c := newTestClient(t, addr, nil)

	if err := c.SendTurn(&llmpb.TurnRequest{SessionId: "s1", TurnId: "t1"}); err != nil {
		t.Fatalf("发起首轮失败: %v", err)
	}
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("服务端未收到首轮")
	}
	if err := c.SendTurn(&llmpb.TurnRequest{SessionId: "s1", TurnId: "t2"}); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("同会话第二轮应被拒绝，实际: %v", err)
	}
	if got := c.Stats().Rejected; got != 1 {
		t.Fatalf("拒绝计数应为 1，实际 %d", got)
	}
	c.CancelTurn("s1")
	waitFor(t, 2*time.Second, func() bool { return c.Inflight() == 0 })
}

// TestCancelTurnStopsStream 取消应让服务端立即结束本轮，并计入取消统计
func TestCancelTurnStopsStream(t *testing.T) {
	fake := &fakeAgent{events: 100, interval: 20 * time.Millisecond, started: make(chan string, 4)}
	addr := startFakeAgent(t, fake)
	got := make(chan *llmpb.TurnResponse, 16)
	c := newTestClient(t, addr, func(r *llmpb.TurnResponse) { got <- r })

	if err := c.SendTurn(&llmpb.TurnRequest{SessionId: "s1", TurnId: "t1"}); err != nil {
		t.Fatalf("发起轮次失败: %v", err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到首个响应")
	}
	if !c.CancelTurn("s1") {
		t.Fatal("取消在途轮次应返回 true")
	}
	waitFor(t, 3*time.Second, func() bool { return atomic.LoadInt32(&fake.cancelled) == 1 })
	waitFor(t, 3*time.Second, func() bool { return c.Inflight() == 0 })
	if got := c.Stats().Cancelled; got != 1 {
		t.Fatalf("取消计数应为 1，实际 %d", got)
	}
}

// TestCloseSessionCancelsInflightAndReleases 释放会话应先取消在途轮次并通知服务端
func TestCloseSessionCancelsInflightAndReleases(t *testing.T) {
	fake := &fakeAgent{events: 100, interval: 20 * time.Millisecond, started: make(chan string, 4)}
	addr := startFakeAgent(t, fake)
	c := newTestClient(t, addr, nil)

	if err := c.SendTurn(&llmpb.TurnRequest{SessionId: "s1", TurnId: "t1"}); err != nil {
		t.Fatalf("发起轮次失败: %v", err)
	}
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("服务端未收到轮次")
	}
	if err := c.CloseSession(context.Background(), "s1", "room_closed"); err != nil {
		t.Fatalf("释放会话失败: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return c.Inflight() == 0 })
	if got := fake.closedSessions(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("服务端应收到 s1 的释放请求，实际 %v", got)
	}
}

// TestSendTurnValidatesRequiredFields 缺少会话或轮次 ID 时应直接拒绝
func TestSendTurnValidatesRequiredFields(t *testing.T) {
	c, err := New(Options{Addr: "127.0.0.1:1", Shards: 1})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SendTurn(&llmpb.TurnRequest{TurnId: "t1"}); err == nil {
		t.Fatal("缺少 session_id 应报错")
	}
	if err := c.SendTurn(&llmpb.TurnRequest{SessionId: "s1"}); err == nil {
		t.Fatal("缺少 turn_id 应报错")
	}
	if got := c.Stats().Sent; got != 0 {
		t.Fatalf("非法请求不应计入已发送，实际 %d", got)
	}
}
