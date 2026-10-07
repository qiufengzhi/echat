// turn.go 轮次级归属：turn 根 span 生命周期 + 端到端/首字/打断延迟的时间戳归属
//
// 机制（docs/observability.md §3.5.5）：turnID 在 asrdispatch 生成时建 span 登记 t0，
// dispatch is_final 或打断时结束；音频归属在 runAITTSLoop 用 AudioEvent.TurnID 判定，
// 全程只做时间戳运算，不跨 goroutine 改 span
package observability

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// turnEntry 一轮的运行时记录
type turnEntry struct {
	span     trace.Span // turn 根 span，EndTurn 时结束
	start    time.Time  // t0：ASR 句末投递时刻
	sentAt   time.Time  // 首句送入 TTS provider 时刻，ttfb 起点
	firstA   bool       // 是否已记过本轮首个音频（只记一次）
	firstTok bool       // 是否已记过本轮首个 LLM token（只记一次）
}

// turnEtc 全局轮次表，键是进程内唯一的 turnID（turnSeq 全局递增保证）
var turnEtc = struct {
	sync.Mutex
	m map[string]*turnEntry
}{m: make(map[string]*turnEntry)}

// interruptReg 打断请求时刻登记表，键是 sessionID（一个会话的播发循环单条）
var interruptReg = struct {
	sync.Mutex
	m map[string]int64 // sessionID -> UnixMilli 新语音到达时刻
}{m: make(map[string]int64)}

// StartTurn 开启一轮：建 turn 根 span（不继承父级，媒体起点无父子关系）并登记 t0
//
// turnID: 本轮唯一 ID
// sessionID: 会话 ID
// roomID: 房间短码
// return: 携带 turn span 的 ctx，供 RPC 与后续埋点延续
func StartTurn(ctx context.Context, turnID, sessionID, roomID string) context.Context {
	tracer := otel.Tracer("echat-backend")
	spanCtx, span := tracer.Start(ctx, "turn",
		trace.WithAttributes(TurnID(turnID), SessionID(sessionID), RoomID(roomID)),
		trace.WithNewRoot(),
	)
	turnEtc.Lock()
	turnEtc.m[turnID] = &turnEntry{span: span, start: time.Now()}
	turnEtc.Unlock()
	return spanCtx
}

// MarkFirstToken 记录本轮首个 LLM token 回 Go 的时刻，记 llm_ttft（t0 → 首 token）
//
// 每个 turnID 只记一次；调用方可对每个含文本的响应调用
// turnID: 轮次 ID
func MarkFirstToken(turnID string) {
	turnEtc.Lock()
	e, ok := turnEtc.m[turnID]
	if !ok || e.firstTok {
		turnEtc.Unlock()
		return
	}
	e.firstTok = true
	start := e.start
	turnEtc.Unlock()
	TurnLLMTTFT.Record(recordContext, time.Since(start).Milliseconds())
}

// MarkFirstSentence 记录本轮首句送入 TTS provider 的时刻（ttfb 起点）
//
// 只在首个 call 生效；靠 turnID 唯一性避免重入污染
// turnID: 轮次 ID
func MarkFirstSentence(turnID string) {
	turnEtc.Lock()
	e, ok := turnEtc.m[turnID]
	if ok && e.sentAt.IsZero() {
		e.sentAt = time.Now()
	}
	turnEtc.Unlock()
}

// MarkFirstAudio 记录本轮首个音频块到达播发循环：由此得 e2e 与 ttfb
//
// 每个 turnID 只记录一次；without sentAt 时仅记 e2e
// turnID: 轮次 ID
func MarkFirstAudio(turnID string) {
	turnEtc.Lock()
	e, ok := turnEtc.m[turnID]
	if !ok || e.firstA {
		turnEtc.Unlock()
		return
	}
	e.firstA = true
	now := time.Now()
	e2e := now.Sub(e.start).Milliseconds()
	sent := e.sentAt
	turnEtc.Unlock()

	TurnE2ELatency.Record(recordContext, e2e)
	if !sent.IsZero() {
		TurnTTSTTFB.Record(recordContext, now.Sub(sent).Milliseconds())
	}
}

// EndTurn 结束一轮：记轮次计数、打断属性并收尾 span，幂等
//
// turnID: 轮次 ID
// end: 终态，取值 completed / interrupted / error
func EndTurn(turnID, end string) {
	turnEtc.Lock()
	e, ok := turnEtc.m[turnID]
	if ok {
		delete(turnEtc.m, turnID)
	} else {
		turnEtc.Unlock()
		return
	}
	span := e.span
	turnEtc.Unlock()

	TurnsTotal.Add(recordContext, 1, metric.WithAttributes(attribute.String("end", end)))

	interrupted := end == "interrupted"
	span.SetAttributes(Bool(AttrInterrupted, interrupted))
	if end == "error" {
		span.SetStatus(codes.Error, "turn failed")
	} else if interrupted {
		span.SetStatus(codes.Ok, "interrupted")
	}
	span.End()
}

// MarkInterruptRequest 打断发生时登记新语音到达时刻（打断延迟起点）
//
// sessionID: 会话 ID
func MarkInterruptRequest(sessionID string) {
	interruptReg.Lock()
	interruptReg.m[sessionID] = time.Now().UnixMilli()
	interruptReg.Unlock()
}

// MarkInterruptDone 播发循环 drain 完成时记录打断延迟 = 此刻 - 请求时刻
//
// sessionID: 会话 ID
func MarkInterruptDone(sessionID string) {
	interruptReg.Lock()
	start, ok := interruptReg.m[sessionID]
	if ok {
		delete(interruptReg.m, sessionID)
	}
	interruptReg.Unlock()
	if !ok {
		return
	}
	TurnInterruptDelay.Record(recordContext, time.Now().UnixMilli()-start)
}
