// bwe.go 拥塞控制指标：目标码率、整形器队列与丢弃
//
// 全部不带 peer/room 维度的 label：活跃对端数量会把 Prometheus 基数顶爆，
// per-peer 归因走结构化日志与 span attribute
package observability

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RecordBweTarget 记录一次目标码率变化
//
// bps: 目标码率，单位 bit/s，非正数忽略
func RecordBweTarget(bps int) {
	if bps > 0 {
		BweTargetBitrate.Record(recordContext, int64(bps))
	}
}

// RecordPacerQueueDepth 记录一次整形器队列深度采样
//
// n: 队列中的包数，负数忽略
func RecordPacerQueueDepth(n int64) {
	if n >= 0 {
		PacerQueueDepth.Record(recordContext, n)
	}
}

// RecordPacerDelay 记录一个包的排队延迟，即入队到出队的时间差
//
// d: 排队时长，负数忽略，内部换算成毫秒
func RecordPacerDelay(d time.Duration) {
	if d >= 0 {
		PacerDelayMs.Record(recordContext, d.Milliseconds())
	}
}

// PacerDroppedAdd 累加整形器丢弃计数
//
// n: 本次丢弃的包数，小于等于 0 忽略
// reason: 丢弃原因，取 queue_full、no_route、closed
func PacerDroppedAdd(n int64, reason string) {
	if n > 0 {
		PacerDropped.Add(recordContext, n, metric.WithAttributes(attribute.String("reason", reason)))
	}
}

// RecordPacerWriteError 记录一次整形器写包失败
func RecordPacerWriteError() {
	PacerWriteErrors.Add(recordContext, 1)
}

// AdjustSubscriberHealth 调整指定健康状态的订阅者计数
//
// state: 健康状态取值 healthy/constrained/degraded/evicted
// delta: 变化量，进入某状态传 1、离开传 -1
func AdjustSubscriberHealth(state string, delta int64) {
	if state == "" || delta == 0 {
		return
	}
	SubscriberHealth.Add(recordContext, delta, metric.WithAttributes(attribute.String("state", state)))
}

// RecordHealthTransition 记录一次健康状态迁移，同时挪动两个状态的计数
//
// from: 迁移前状态
// to: 迁移后状态
func RecordHealthTransition(from, to string) {
	AdjustSubscriberHealth(from, -1)
	AdjustSubscriberHealth(to, 1)
	HealthTransitions.Add(recordContext, 1, metric.WithAttributes(attribute.String("state", to)))
}

// RecordOpusBitrate 记录一次房间级编码码率
//
// bps: 编码码率，单位 bit/s，非正数忽略
func RecordOpusBitrate(bps int) {
	if bps > 0 {
		OpusEncoderBitrate.Record(recordContext, int64(bps))
	}
}

// RecordBitrateAction 记录一次房间级码率档位调整
//
// action: 动作名称，取 up 或 down
func RecordBitrateAction(action string) {
	if action == "" {
		return
	}
	BitrateActions.Add(recordContext, 1, metric.WithAttributes(attribute.String("action", action)))
}
