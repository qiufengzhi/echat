package observability

import (
	"context"
	"strings"
	"time"

	"echat-backend/config"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	// defaultMetricsInterval 未配置指标导出间隔时的值
	defaultMetricsInterval = 15 * time.Second
	// metricInstrumentBase 本包指标 Instrumentation 名
	metricInstrumentBase = "echat-backend"
)

// initMetrics 搭指标链：OTLP gRPC exporter + 周期读（PeriodicReader）
//
// 导出间隔配置非法或为空时取默认 15s
// cfg: 配置段，取 OTLPEndpoint 与 MetricsExportInterval
// res: 进程 Resource
// return: MeterProvider 的清理函数与错误
func initMetrics(cfg config.ObservabilityConfig, res *resource.Resource) (func(context.Context) error, error) {
	// 与 trace 同理：明文 HTTP/2，显式 insecure
	exp, err := otlpmetricgrpc.New(context.Background(),
		otlpmetricgrpc.WithEndpoint(endpointOf(cfg)),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	interval := parseDuration(cfg.MetricsExportInterval, defaultMetricsInterval)
	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(exp, metric.WithInterval(interval))),
	)
	otel.SetMeterProvider(mp)
	return mp.Shutdown, nil
}

// endpointOf 返回 OTLP 端点，未配置时取默认 localhost:4317
//
// cfg: 配置段，取 OTLPEndpoint
// return: host:port 形式的端点
func endpointOf(cfg config.ObservabilityConfig) string {
	if strings.TrimSpace(cfg.OTLPEndpoint) == "" {
		return defaultEndpoint
	}
	return cfg.OTLPEndpoint
}

// effectiveRate 规整采样率：0 或超出合法区间按全采 1.0 处理
//
// rate: 配置的采样率
// return: 0.0-1.0 之间的合法值
func effectiveRate(rate float64) float64 {
	if rate <= 0 || rate > 1 {
		return 1.0
	}
	return rate
}

// parseDuration 解析时长字符串，非法或非正数时返回兜底值
//
// raw: 形如 15s、500ms 的时长
// fallback: 解析失败时的返回值
// return: 解析结果
func parseDuration(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
