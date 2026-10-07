"""OTel 可观测性初始化：trace / metrics 经 OTLP 推向 Collector

与应用其余部分解耦：本模块只装配全局 Provider / Exporter，
业务埋点处用
    from opentelemetry import trace, metrics
取全局 tracer / meter，不反向依赖本模块
应用只认 OTLP 一个出口（docs/observability.md），日志信号由 T1-8 接入
"""

from __future__ import annotations

import logging
import socket
from typing import Optional

# 用下划线别名避免遮蔽同包子模块名 observability.metrics / observability.attrs
from opentelemetry import metrics as _otel_metrics, trace as _otel_trace
from opentelemetry._logs import LogRecord
try:
    from opentelemetry.exporter.otlp.proto.grpc.log_exporter import OTLPLogExporter
except ImportError:  # noqa 旧版 otel 的导出模块带下划线前缀
    from opentelemetry.exporter.otlp.proto.grpc._log_exporter import (  # type: ignore
        OTLPLogExporter,
    )
from opentelemetry.sdk._logs import LoggerProvider as LogsLoggerProvider
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor as LogBatchProcessor
from opentelemetry.trace import TraceFlags
from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk.trace.sampling import ParentBased, TraceIdRatioBased

from agent.config.loader import ObservabilityConfig

logger = logging.getLogger("obs")

# 日志链路：loguru 与 zap 相同，落盘保留、另接一条 OTLP Logs 推 Collector → Loki
_otel_logger = None  # loguru sink 使用的 OTel Logger

# defaultEndpoint OTLP 端点兜底，与 observability compose 对齐
DEFAULT_ENDPOINT = "localhost:4317"
# defaultMetricsIntervalMs 指标导出间隔兜底（毫秒）
DEFAULT_METRICS_INTERVAL_MS = 15000


def setup(cfg: Optional[ObservabilityConfig]) -> None:
    """初始化全局 Tracer / Meter Provider 并接 OTLP

    进程只认 OTLP 一个出口，不内置 Prometheus exporter；
    采样率 0 或超出合法区间按全采处理
    cfg: observability 配置段，None 或未启用时不初始化
    """
    if cfg is None or not cfg.enabled:
        return
    endpoint = cfg.otlp_endpoint.strip() or DEFAULT_ENDPOINT
    resource = Resource.create(
        {
            "service.name": (cfg.service_name or "echat-agent"),
            "service.version": "dev",
            "host.name": socket.gethostname(),
        }
    )
    rate = cfg.trace_sample_rate
    if rate <= 0 or rate > 1:
        rate = 1.0

    tracer_provider = TracerProvider(
        resource=resource,
        sampler=ParentBased(TraceIdRatioBased(rate)),
    )
    tracer_provider.add_span_processor(
        BatchSpanProcessor(OTLPSpanExporter(endpoint=endpoint, insecure=True))
    )
    _otel_trace.set_tracer_provider(tracer_provider)

    reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(endpoint=endpoint, insecure=True),
        export_interval_millis=_interval_ms(cfg.metrics_export_interval),
    )
    _otel_metrics.set_meter_provider(MeterProvider(resource=resource, metric_readers=[reader]))
    setup_logs(cfg)

    logger.info(
        "可观测性已启用 service=%s endpoint=%s rate=%s",
        resource.attributes.get("service.name"),
        endpoint,
        rate,
    )


def _interval_ms(raw: str) -> int:
    """把形如 15s / 500ms 的间隔字符串转毫秒，非法取默认值

    raw: 配置的间隔
    return: 毫秒数
    """
    raw = (raw or "").strip().lower()
    try:
        if raw.endswith("ms"):
            val = int(raw[:-2])
            if val > 0:
                return val
        elif raw.endswith("s"):
            val = float(raw[:-1]) * 1000
            if val > 0:
                return int(val)
    except ValueError:
        pass
    return DEFAULT_METRICS_INTERVAL_MS


def setup_logs(cfg: Optional[ObservabilityConfig]) -> None:
    """搭 OTLP Logs 链路：LoggerProvider + 批量 processor，供 loguru sink 推送

    cfg: observability 配置段，None 或 logs_enabled 为假时不初始化
    """
    global _otel_logger
    if cfg is None or not cfg.logs_enabled:
        return
    endpoint = (cfg.otlp_endpoint or "").strip() or DEFAULT_ENDPOINT
    exporter = OTLPLogExporter(endpoint=endpoint, insecure=True)
    provider = LogsLoggerProvider(
        resource=Resource.create(
            {
                "service.name": cfg.service_name or "echat-agent",
                "service.version": "dev",
                "host.name": socket.gethostname(),
            }
        )
    )
    provider.add_log_record_processor(LogBatchProcessor(exporter))
    _otel_logger = provider.get_logger("echat-agent")


def log_sink(message) -> None:
    """loguru sink：把一条 record 转成 OTel LogRecord，带当前 span 的 trace 上下文

    message: loguru 的 Message 对象，含 .record 字段
    """
    if _otel_logger is None:
        return
    record = message.record
    sc = _otel_trace.get_current_span().get_span_context()
    body = record["message"]
    if record.get("exception") is not None:
        body = f"{body} | exception: {record['exception']['value']}"
    lr = LogRecord(
        timestamp=int(record["time"].timestamp() * 1_000_000_000),
        trace_id=sc.trace_id if sc.is_valid else None,
        span_id=sc.span_id if sc.is_valid else None,
        trace_flags=TraceFlags.SAMPLED if sc.is_valid else TraceFlags.DEFAULT,
        severity_text=record["level"].name,
        severity_number=int(record["level"].no),
        body=body,
        attributes={"session": (record.get("extra") or {}).get("session", "")},
    )
    _otel_logger.emit(lr)