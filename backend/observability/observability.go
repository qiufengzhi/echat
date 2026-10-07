// Package observability 统一接入 OpenTelemetry：trace / metrics 经 OTLP 推向 Collector
//
// 设计约束（见 docs/observability.md）：
//   - 应用只认 OTLP 一个出口，不内置 Prometheus exporter
//   - 所有 attribute key 常量收在本包 attrs.go，业务代码不写裸字符串
//   - 指标在 init 期一次性注册，不在热路径 new
//   - 日志（OTLP Logs → Loki）由 logs.go 单独接入，见 T1-8
package observability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"echat-backend/config"
	"echat-backend/logging"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var logger = logging.New("obs")

// ServiceVersion 写进 service.version 的构建号，便于定位日志与 trace 归属
const ServiceVersion = "dev"

// defaultEndpoint 未配置 OTLP 端点时的兜底地址，与 observability compose 对齐
const defaultEndpoint = "localhost:4317"

// shutdownFunc 进程退出时的清理函数集合，按逆序执行（先停 span/metric，再关 exporter）
type shutdownFunc struct {
	fns []func(context.Context) error
}

// add 追加一个清理函数
//
// f: 幂等清理函数
func (s *shutdownFunc) add(f func(context.Context) error) {
	s.fns = append(s.fns, f)
}

// shutdown 按注册逆序执行全部清理，带整体超时
//
// timeout: 总清理超时
// return: 首个出现且不是 context 取消的错误
func (s *shutdownFunc) shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var first error
	for i := len(s.fns) - 1; i >= 0; i-- {
		if err := s.fns[i](ctx); err != nil && first == nil && !errors.Is(err, context.Canceled) {
			first = err
		}
	}
	return first
}

// Init 初始化全局 TracerProvider / MeterProvider 并接入 OTLP 出口
//
// 采样率、导出间隔在 cfg 上未设置时取默认；进程退出由调用方 defer 返回的 shutdown
// cfg: observability 配置段
// return: 清理函数与错误，禁用或被禁时清理函数为空操作
func Init(cfg config.ObservabilityConfig) (func(), error) {
	if !cfg.Enabled {
		return func() {}, nil
	}
	logger.Infow("可观测性已启用",
		"endpoint", endpointOf(cfg),
		"sampleRate", effectiveRate(cfg.TraceSampleRate),
		"captureContent", cfg.CaptureContent)
	// 导出内层错误默认打到 stderr，转到业务日志便于排查
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warnw("OTel 导出错误", "error", err)
	}))
	cleanup := &shutdownFunc{}

	res, err := buildResource(cfg.ServiceName)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	traceShutdown, err := initTrace(cfg, res)
	if err != nil {
		return nil, err
	}
	cleanup.add(traceShutdown)

	metricShutdown, err := initMetrics(cfg, res)
	if err != nil {
		_ = cleanup.shutdown(10 * time.Second)
		return nil, err
	}
	cleanup.add(metricShutdown)

	// 把 W3C 传播器设为全局，otelgrpc 与手动 extract 都依赖它
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// Go runtime 指标（GC/goroutine/heap）走标准 OTel 语义，覆盖资源层一档
	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
		logger.Warnw("runtime 指标启动失败", "error", err)
	}

	return func() { _ = cleanup.shutdown(10 * time.Second) }, nil
}

// buildResource 构造 OTel Resource：service.name / version / host
//
// 不并入 resource.Default()：其内部 semconv 版本与这里引入的版本可能 schema 冲突
// serviceName: 上报的服务名，空时取进程名兜底
// return: Resource 实例
func buildResource(serviceName string) (*resource.Resource, error) {
	if serviceName == "" {
		serviceName = "echat-backend"
	}
	host, _ := os.Hostname()
	return resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(ServiceVersion),
		semconv.HostName(host),
	), nil
}
