// bridge.go 把 dataplane 运行快照桥接成 OTLP 指标
//
// dataplane 名字里嵌着会话/房间 ID，若直接当 label 会把 ID 带进时序库（基数失控），
// 必须按「去掉末尾 ID 段」聚合成有限计数族（docs/observability.md §3.3）
package observability

import (
	"context"
	"strings"
	"time"

	"echat-backend/dataplane"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// bridgeInterval 快照桥接周期，按积压连续量取值，周期不必过短
const bridgeInterval = 10 * time.Second

// startBridge 启动周期桥接协程，ctx 取消时退出
//
// ctx: 进程退出信号
func startBridge(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(bridgeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				emitSnapshot()
			}
		}
	}()
}

// emitSnapshot 读一次快照并记录；ring 记积压/容量/丢弃，counter 记丢弃
func emitSnapshot() {
	for _, e := range dataplane.Snapshot() {
		family := familyOf(e.Name)
		switch e.Kind {
		case "ring":
			opts := metric.WithAttributes(attribute.String("queue", family))
			RingBacklog.Record(recordContext, int64(e.Backlog), opts)
			RingCapacity.Record(recordContext, int64(e.Capacity), opts)
			DataplaneDropped.Record(recordContext, e.Dropped, opts)
		default:
			DataplaneDropped.Record(
				recordContext, e.Dropped,
				metric.WithAttributes(attribute.String("counter", family)))
		}
	}
}

// familyOf 去掉名字末尾的 ID 段得到有限计数族
//
// "sfu.asr.<sid>" -> "sfu.asr"；"asr.input"（无 ID 段）保持原样
// name: dataplane 结构名
// return: 聚合后的族名
func familyOf(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx <= 0 {
		return name
	}
	return name[:idx]
}
