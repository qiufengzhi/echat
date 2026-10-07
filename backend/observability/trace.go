package observability

import (
	"context"

	"echat-backend/config"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
)

// initTrace 搭 trace 导出链：OTLP gRPC exporter + ParentBased(Ratio) 采样 + 批量 processor
//
// 采样率 0 或超出合法区间按全采处理；dev 建议 1.0，prod 建议 0.1
// cfg: 配置段，取 OTLPEndpoint 与 TraceSampleRate
// res: 进程 Resource
// return: TracerProvider 的清理函数与错误
func initTrace(cfg config.ObservabilityConfig, res *resource.Resource) (func(context.Context) error, error) {
	// OTLP gRPC 默认走 TLS，本机 Collector 是明文 HTTP/2，必须显式 insecure
	exp, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(endpointOf(cfg)),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	rate := effectiveRate(cfg.TraceSampleRate)
	tp := trace.NewTracerProvider(
		trace.WithResource(res),
		trace.WithSampler(trace.ParentBased(trace.TraceIDRatioBased(rate))),
		trace.WithBatcher(exp),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
