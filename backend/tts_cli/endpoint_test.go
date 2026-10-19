package tts_cli

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	llmpb "echat-backend/proto/llm"
)

// newTestService 构造一个使用假凭证合成器的 Service
//
// 假凭证下 CreateSession 只建表与起协程，不发起任何网络请求；
// 测试用不含句末符的文本，避免真正触发云端合成
// t: 测试上下文，用于注册清理
// return: 全新的 Service 实例，测试结束后全局 provider 与 GlobalTTSService 会被还原
func newTestService(t *testing.T) *Service {
	t.Helper()

	syn, err := NewAliyunSynthesizer(AliyunTTSConfig{
		AccessKeyID:     "test-ak",
		AccessKeySecret: "test-sk",
		AppKey:          "test-appkey",
		Voice:           "Alicia",
		SampleRate:      16000,
	})
	if err != nil {
		t.Fatalf("构造合成器失败: %v", err)
	}

	prevProvider, prevService := provider, GlobalTTSService
	t.Cleanup(func() {
		provider = prevProvider
		GlobalTTSService = prevService
	})

	provider = syn
	svc := &Service{
		buffer:    make(map[string]*sentenceBuffer),
		endpoints: make(map[string]*endpoint),
	}
	GlobalTTSService = svc
	return svc
}

// TestEndpointInterruptEventFollowsInFlightAudio 打断事件排在所有在途音频之后
func TestEndpointInterruptEventFollowsInFlightAudio(t *testing.T) {
	ep := newEndpoint("s-order", 8)
	defer ep.close()

	sp := &SessionPipeline{}
	ep.beginTurn("t1")
	ep.attach(sp)

	ep.publishFrom(sp, synthChunk{pcm: []byte{1}})
	ep.publishFrom(sp, synthChunk{pcm: []byte{2}})
	ep.interrupt()

	want := []AudioEventKind{AudioData, AudioData, AudioInterrupt}
	for i, wantKind := range want {
		ev, ok := ep.Next()
		if !ok {
			t.Fatalf("第 %d 个事件缺失", i)
		}
		if ev.Kind != wantKind {
			t.Fatalf("第 %d 个事件期望 %s，得到 %s", i, wantKind, ev.Kind)
		}
	}
	if _, ok := ep.Next(); ok {
		t.Fatal("打断后不应还有事件")
	}
}

// TestEndpointDropsAudioPublishedAfterInterrupt 打断后到达的在途音频被丢弃并计数
func TestEndpointDropsAudioPublishedAfterInterrupt(t *testing.T) {
	ep := newEndpoint("s-late", 8)
	defer ep.close()

	sp := &SessionPipeline{}
	ep.beginTurn("t1")
	ep.attach(sp)
	ep.interrupt()

	// 打断事件本身会入队，先把它取走，再看在途音频是否被拦下
	if ev, ok := ep.Next(); !ok || ev.Kind != AudioInterrupt {
		t.Fatalf("期望先读到打断事件，得到 %+v（ok=%v）", ev, ok)
	}

	before := staleSegmentDropped.Value()
	ep.publishFrom(sp, synthChunk{pcm: []byte{1}})
	ep.publishFrom(sp, synthChunk{pcm: []byte{2}})

	if got := staleSegmentDropped.Value() - before; got != 2 {
		t.Fatalf("期望计数增加 2，实际增加 %d", got)
	}
	if _, ok := ep.Next(); ok {
		t.Fatal("被打断轮次的音频不应进入事件流")
	}
}

// TestEndpointResumesOnNextTurn 打断之后端点仍可用，新轮事件可达
//
// 这条是 DF-01 的判据：打断曾经销毁订阅点，导致此后整条连接再也无人消费音频
func TestEndpointResumesOnNextTurn(t *testing.T) {
	ep := newEndpoint("s-resume", 8)
	defer ep.close()

	first := &SessionPipeline{}
	ep.beginTurn("t1")
	ep.attach(first)
	ep.publishFrom(first, synthChunk{pcm: []byte{1}})
	ep.interrupt()
	if !ep.isDead("t1") {
		t.Fatal("被打断的轮次应被标为作废")
	}

	second := &SessionPipeline{}
	ep.beginTurn("t2")
	ep.attach(second)
	ep.publishFrom(second, synthChunk{pcm: []byte{2}})

	// 依次应读到：t1 的音频、打断事件、t2 的音频
	kinds := []AudioEventKind{AudioData, AudioInterrupt, AudioData}
	for i, want := range kinds {
		ev, ok := ep.Next()
		if !ok {
			t.Fatalf("第 %d 个事件缺失", i)
		}
		if ev.Kind != want {
			t.Fatalf("第 %d 个事件期望 %s，得到 %s", i, want, ev.Kind)
		}
	}

	// 新轮也应能正常被打断
	ep.interrupt()
	if ev, ok := ep.Next(); !ok || ev.Kind != AudioInterrupt {
		t.Fatal("新轮应能正常被打断")
	}
}

// TestEndpointDropsAudioFromReplacedExecutor 已被换代的执行器输出的音频被丢弃
func TestEndpointDropsAudioFromReplacedExecutor(t *testing.T) {
	ep := newEndpoint("s-replace", 8)
	defer ep.close()

	old := &SessionPipeline{}
	ep.beginTurn("t1")
	ep.attach(old)

	current := &SessionPipeline{}
	if !ep.attach(current) {
		t.Fatal("新执行器应被登记为当前泵源")
	}
	if ep.attach(current) {
		t.Fatal("同一执行器重复登记应返回 false，避免重复挂泵")
	}

	before := staleSegmentDropped.Value()
	ep.publishFrom(old, synthChunk{pcm: []byte{9}})
	if got := staleSegmentDropped.Value() - before; got != 1 {
		t.Fatalf("旧执行器的输出应被丢弃并计数，实际增加 %d", got)
	}

	ep.publishFrom(current, synthChunk{pcm: []byte{7}})
	ev, ok := ep.Next()
	if !ok || ev.Kind != AudioData || !bytes.Equal(ev.PCM, []byte{7}) {
		t.Fatalf("当前执行器的输出应正常入队，得到 %+v（ok=%v）", ev, ok)
	}
}

// TestEndpointDeadTurnSetIsBounded 作废轮次集合有上限，不会随会话无限增长
func TestEndpointDeadTurnSetIsBounded(t *testing.T) {
	ep := newEndpoint("s-dead-limit", 4)
	defer ep.close()

	for i := 0; i < deadTurnLimit+8; i++ {
		ep.markDead(turnIDOf(i))
	}
	if len(ep.deadOrder) != deadTurnLimit {
		t.Fatalf("作废轮次集合应被限制在 %d，实际 %d", deadTurnLimit, len(ep.deadOrder))
	}
	if ep.isDead(turnIDOf(0)) {
		t.Fatal("最早作废的轮次应已被淘汰出集合")
	}
	if !ep.isDead(turnIDOf(deadTurnLimit + 7)) {
		t.Fatal("最新作废的轮次应在集合内")
	}
}

// turnIDOf 生成测试用的轮次标识
//
// i: 轮次序号
// return: 形如 t<i> 的标识
func turnIDOf(i int) string {
	return fmt.Sprintf("t%d", i)
}

// TestProcessTextDropsLateTokenOfSupersededTurn 迟到 token 不得混进新轮缓冲
//
// 这是「取消 RPC 是尽力而为」在文本侧的接缝：旧轮的 token 会晚于新轮开始到达
func TestProcessTextDropsLateTokenOfSupersededTurn(t *testing.T) {
	svc := newTestService(t)
	const session = "s-turn"
	defer svc.Close(session)

	svc.ProcessText(&llmpb.TurnResponse{SessionId: session, TurnId: "t1", ResponseText: "你好"})
	svc.ProcessText(&llmpb.TurnResponse{SessionId: session, TurnId: "t2", ResponseText: "世界"})

	before := staleTokenDropped.Value()
	svc.ProcessText(&llmpb.TurnResponse{SessionId: session, TurnId: "t1", ResponseText: "迟到"})
	if got := staleTokenDropped.Value() - before; got != 1 {
		t.Fatalf("迟到 token 应被丢弃并计数，实际增加 %d", got)
	}

	sb, ok := svc.buffer[session]
	if !ok {
		t.Fatal("新轮的缓冲不应被删除")
	}
	if sb.turnID != "t2" {
		t.Fatalf("缓冲应属于新轮 t2，实际 %s", sb.turnID)
	}
	if got := sb.text.String(); got != "世界" {
		t.Fatalf("迟到 token 混进了新轮缓冲：%q", got)
	}
}

// TestProcessTextDropsTokenOfFinishedTurn 已收尾轮次的迟到 token 被丢弃
func TestProcessTextDropsTokenOfFinishedTurn(t *testing.T) {
	svc := newTestService(t)
	const session = "s-finished"
	defer svc.Close(session)

	ep := svc.AudioEndpoint(session)
	ep.markDead("t1")

	before := staleTokenDropped.Value()
	svc.ProcessText(&llmpb.TurnResponse{SessionId: session, TurnId: "t1", ResponseText: "你好"})
	if got := staleTokenDropped.Value() - before; got != 1 {
		t.Fatalf("已收尾轮次的迟到 token 应被丢弃，实际增加 %d", got)
	}
	if _, ok := svc.buffer[session]; ok {
		t.Fatal("迟到 token 不应重新建立文本缓冲")
	}
}

// TestEndpointCloseWakesConsumer 端点关闭后消费循环应能自行退出，不留死等
func TestEndpointCloseWakesConsumer(t *testing.T) {
	svc := newTestService(t)
	const session = "s-close"

	ep := svc.AudioEndpoint(session)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := ep.Next(); ok {
				continue
			}
			if ep.Closed() {
				return
			}
			select {
			case <-ep.Wait():
			case <-time.After(2 * time.Second):
				return
			}
		}
	}()

	svc.Close(session)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("端点关闭后消费循环未退出")
	}
}
