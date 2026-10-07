// logs.go OTLP Logs 信号：把 zap 日志桥接成 OTel LogRecord → Collector → Loki
//
// 不做第二套 logger：落盘照旧（zap 原有 core），另加一条 otelzap core 进 Tee，
// 日志正文仍遵循「禁放对话内容」的 PII 口径（docs/observability.md §3.2.3）
package observability

import (
	"context"
	"strings"

	"echat-backend/config"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap/zapcore"
)

// logsEnabled OTLP 日志链路是否可用（日志未启用时 LogExtraCore 返回空）
var logsEnabled bool

// logMinLevel OTLP 日志链路的级别下限，由 initLogs 按配置解析
var logMinLevel = zapcore.WarnLevel

// noopShutdown 空清理函数
func noopShutdown(context.Context) error { return nil }

// initLogs 搭 OTLP Logs 链路：LoggerProvider + 批量 processor，并设为全局供 otelzap 取用
//
// 仅在 logs_enabled 时启用；日志落盘始终保留
// cfg: observability 配置段，取 LogsEnabled / LogsMinLevel / OTLPEndpoint
// res: 进程 Resource
// return: LoggerProvider 的清理函数与错误
func initLogs(cfg config.ObservabilityConfig, res *resource.Resource) (func(context.Context) error, error) {
	if !cfg.LogsEnabled {
		return noopShutdown, nil
	}
	exp, err := otlploggrpc.New(context.Background(),
		otlploggrpc.WithEndpoint(endpointOf(cfg)),
		otlploggrpc.WithInsecure(), // 同 trace/metrics：明文 HTTP/2
	)
	if err != nil {
		return nil, err
	}
	logsEnabled = true
	logMinLevel = parseZapLevel(cfg.LogsMinLevel)
	prov := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	otel.SetLoggerProvider(prov)
	return prov.Shutdown, nil
}

// LogExtraCore 返回把 zap 日志桥进 OTLP Logs 的 core，按 logs_min_level 过滤
//
// return: core 与是否可用（日志未启用时为 false，nil 表示不追加）
func LogExtraCore() (zapcore.Core, bool) {
	if !logsEnabled {
		return nil, false
	}
	return &levelGateCore{core: otelzap.NewCore("echat-backend"), min: logMinLevel}, true
}

// levelGateCore 按级别门控包装，拦下低于下限的日志不进 OTLP 链路
type levelGateCore struct {
	core zapcore.Core  // 被包装的 otelzap core
	min  zapcore.Level // 级别下限
}

// Enabled 判断级别是否达到下限
//
// l: 待判断级别
// return: 达到下限返回 true
func (c *levelGateCore) Enabled(l zapcore.Level) bool { return l >= c.min }

// With 追加字段后返回同样门控的包装
//
// f: 追加字段
// return: 包装后的 core
func (c *levelGateCore) With(f []zapcore.Field) zapcore.Core {
	return &levelGateCore{core: c.core.With(f), min: c.min}
}

// Check 达下限才继续检查
//
// e: 日志条目
// ce: 已检查条目
// return: 未达下限时原样返回，达到时交给内层 core
func (c *levelGateCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if !c.Enabled(e.Level) {
		return ce
	}
	return c.core.Check(e, ce)
}

// Write 透写内层 core
//
// e: 日志条目
// f: 字段
// return: 内层写入错误
func (c *levelGateCore) Write(e zapcore.Entry, f []zapcore.Field) error { return c.core.Write(e, f) }

// Sync 透写内层同步
//
// return: 内层同步错误
func (c *levelGateCore) Sync() error { return c.core.Sync() }

// parseZapLevel 解析日志级别下限字符串
//
// s: 取值 debug | info | warn | error，未知值按 info
// return: zap 级别
func parseZapLevel(s string) zapcore.Level {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
