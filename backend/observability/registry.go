// registry.go 指标注册表：Init 期一次性创建全部 instrument，热路径只 record 不 new
//
// 指标名与 docs/observability.md §3.4 对齐；时长统一毫秒并在名里显式不写 _ms（单位经 WithUnit 表达）
// 禁用可观测性时这些 instrument 绑定到 noop meter，record 为空操作，调用方无需判空
package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// metricNameSuffix 不需要
var (
	// TurnE2ELatency 服务端端到端：ASR 句末 → 本轮首块音频到达播发循环
	TurnE2ELatency metric.Int64Histogram
	// TurnLLMTTFT LLM 首 token：ASR 句末 → 首个 token 回 Go
	TurnLLMTTFT metric.Int64Histogram
	// TurnTTSTTFB TTS 首块：首句送入 provider → 本轮首块音频
	TurnTTSTTFB metric.Int64Histogram
	// TurnInterruptDelay 打断响应：新语音到达 → 播发循环 drain 完成
	TurnInterruptDelay metric.Int64Histogram
	// TurnFirstAudioEncode 首块音频 → 首帧 RTP（播发循环内统计）
	TurnFirstAudioEncode metric.Int64Histogram

	// TurnsTotal 轮次计数，label end=completed|interrupted|error
	TurnsTotal metric.Int64Counter
	// AgentInflight 当前在途轮次（agentcli 注册表）
	AgentInflight metric.Int64Gauge
	// AgentRetried 分片改投重试次数
	AgentRetried metric.Int64Counter

	// RelayWriteErrors 写中继轨失败
	RelayWriteErrors metric.Int64Counter
	// OpusDecodeErrors 本地 Opus 解码失败
	OpusDecodeErrors metric.Int64Counter
	// AIWriteErrors 写 AI TTS 轨失败
	AIWriteErrors metric.Int64Counter
	// RedisFallback Redis 不可用降级进程内
	RedisFallback metric.Int64Counter

	// RingBacklog 数据面队列当前积压，label queue=聚合后的队列族
	RingBacklog metric.Int64Gauge
	// RingCapacity 数据面队列容量
	RingCapacity metric.Int64Gauge
	// DataplaneDropped 数据面累计丢弃，label counter=聚合后的计数/队列族
	DataplaneDropped metric.Int64Gauge
)

// init 预建 noop instrument：任何未初始化（单元测试、禁用）的运行下 recorder 不 panic
// 运行时 Init 会在真实 MeterProvider 就绪后重绑
func init() {
	initInstruments()
}

// initInstruments 在采集器就绪后创建全部 instrument；幂等，可安全重复调用
//
// 必须在 SetMeterProvider 之后调用一次，否则绑定到 noop
func initInstruments() {
	meter := otel.Meter("echat-backend")

	TurnE2ELatency = mustInt64Histogram(meter, "echat.turn.e2e_latency", "ms", "服务端端到端：ASR 句末到本轮首块音频到达播发循环")
	TurnLLMTTFT = mustInt64Histogram(meter, "echat.turn.llm_ttft", "ms", "LLM 首 token：ASR 句末到首个 token 回 Go")
	TurnTTSTTFB = mustInt64Histogram(meter, "echat.turn.tts_ttfb", "ms", "TTS 首块：首句送入 provider 到本轮首块音频")
	TurnInterruptDelay = mustInt64Histogram(meter, "echat.turn.interrupt_delay", "ms", "打断响应：新语音到达到播发循环 drain 完成")
	TurnFirstAudioEncode = mustInt64Histogram(meter, "echat.turn.first_audio_encode", "ms", "首块音频到首帧 RTP 写出")

	TurnsTotal = mustInt64Counter(meter, "echat.turns_total", "轮次，label end 取值 completed/interrupted/error")
	AgentInflight = mustInt64Gauge(meter, "echat.agentcli.inflight_turns", "当前在途轮次数")
	AgentRetried = mustInt64Counter(meter, "echat.agentcli.retried_total", "分片改投重试次数")

	RelayWriteErrors = mustInt64Counter(meter, "echat.sfu.relay_write_errors_total", "写中继轨失败次数")
	OpusDecodeErrors = mustInt64Counter(meter, "echat.sfu.opus_decode_errors_total", "本地 Opus 解码失败次数")
	AIWriteErrors = mustInt64Counter(meter, "echat.sfu.ai_write_errors_total", "写 AI TTS 轨失败次数")
	RedisFallback = mustInt64Counter(meter, "echat.redis.fallback_total", "Redis 不可用降级进程内次数")

	RingBacklog = mustInt64Gauge(meter, "echat.dataplane.ring_backlog", "数据面队列当前积压")
	RingCapacity = mustInt64Gauge(meter, "echat.dataplane.ring_capacity", "数据面队列容量")
	DataplaneDropped = mustInt64Gauge(meter, "echat.dataplane.dropped", "数据面累计丢弃")
}

func mustInt64Histogram(m metric.Meter, name, unit, desc string) metric.Int64Histogram {
	h, err := m.Int64Histogram(name, metric.WithUnit(unit), metric.WithDescription(desc))
	if err != nil {
		panic(err)
	}
	return h
}

func mustInt64Counter(m metric.Meter, name, desc string) metric.Int64Counter {
	h, err := m.Int64Counter(name, metric.WithDescription(desc))
	if err != nil {
		panic(err)
	}
	return h
}

func mustInt64Gauge(m metric.Meter, name, desc string) metric.Int64Gauge {
	g, err := m.Int64Gauge(name, metric.WithDescription(desc))
	if err != nil {
		panic(err)
	}
	return g
}

// recordContext 记录指标用的 context，桥接协程与热路径统一用它
var recordContext = context.Background()
