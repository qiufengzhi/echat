"""Python 侧指标：懒创建一次、热路径只 record（进程级缓存）"""

from __future__ import annotations

from opentelemetry import metrics

_gen_ai_ttfc = None  # gen_ai.client.operation.time_to_first_chunk
_gen_ai_dur = None  # gen_ai.client.operation.duration
_gen_ai_tokens = None  # gen_ai.client.token.usage
_echat_llm_ttft = None  # echat.llm.ttft（Python 侧模型腿首 token，ms）


def _ensure() -> None:
    """进程内一次创建指标（首次使用前 setup 已把全局 MeterProvider 就位）"""
    global _gen_ai_ttfc, _gen_ai_dur, _gen_ai_tokens, _echat_llm_ttft
    if _gen_ai_ttfc is not None:
        return
    meter = metrics.get_meter("echat-agent")
    _gen_ai_ttfc = meter.create_histogram(
        "gen_ai.client.operation.time_to_first_chunk",
        unit="s",
        description="LLM 请求发出到首个分片返回",
    )
    _gen_ai_dur = meter.create_histogram(
        "gen_ai.client.operation.duration", unit="s", description="LLM 请求总耗时"
    )
    _gen_ai_tokens = meter.create_histogram(
        "gen_ai.client.token.usage", description="LLM token 用量，label gen_ai.token.type"
    )
    _echat_llm_ttft = meter.create_histogram(
        "echat.llm.ttft", unit="ms", description="Python 侧模型首 token 延迟"
    )


def ttfc() -> metrics.Histogram:
    """取首包延迟直方图"""
    _ensure()
    return _gen_ai_ttfc


def dur() -> metrics.Histogram:
    """取请求总耗时直方图"""
    _ensure()
    return _gen_ai_dur


def tokens() -> metrics.Histogram:
    """取 token 用量直方图"""
    _ensure()
    return _gen_ai_tokens


def llm_ttft() -> metrics.Histogram:
    """取本地模型首 token 直方图"""
    _ensure()
    return _echat_llm_ttft