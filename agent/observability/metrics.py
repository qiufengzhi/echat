"""Python 侧指标：懒创建一次、热路径只 record（进程级缓存）"""

from __future__ import annotations

from opentelemetry import metrics

_gen_ai_ttfc = None  # gen_ai.client.operation.time_to_first_chunk
_gen_ai_dur = None  # gen_ai.client.operation.duration
_gen_ai_tokens = None  # gen_ai.client.token.usage
_echat_llm_ttft = None  # echat.llm.ttft（Python 侧模型环节首 token，ms）
_llm_requests = None  # echat.llm.requests_total（label status）
_agent_sessions = None  # echat.agent.sessions_active
_agent_pool_full = None  # echat.agent.session_pool_full_total
_agent_evicted = None  # echat.agent.sessions_evicted_total
_upstream_wait = None  # echat.agent.upstream_wait_ms


def _ensure() -> None:
    """进程内一次创建指标（首次使用前 setup 已把全局 MeterProvider 就位）"""
    global _gen_ai_ttfc, _gen_ai_dur, _gen_ai_tokens, _echat_llm_ttft
    global _llm_requests, _agent_sessions, _agent_pool_full, _agent_evicted, _upstream_wait
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
    _llm_requests = meter.create_counter(
        "echat.llm.requests_total", description="LLM 请求数，label status=ok|cancelled|error"
    )
    _agent_sessions = meter.create_up_down_counter(
        "echat.agent.sessions_active", description="当前活跃会话数"
    )
    _agent_pool_full = meter.create_counter(
        "echat.agent.session_pool_full_total", description="会话池满被拒次数"
    )
    _agent_evicted = meter.create_counter(
        "echat.agent.sessions_evicted_total", description="过期会话淘汰次数"
    )
    _upstream_wait = meter.create_histogram(
        "echat.agent.upstream_wait_ms", unit="ms", description="上游并发闸门排队等待"
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


def llm_requests():
    """取 LLM 请求数计数器（label status）"""
    _ensure()
    return _llm_requests


def agent_sessions():
    """取活跃会话数（UpDownCounter）"""
    _ensure()
    return _agent_sessions


def agent_pool_full():
    """取会话池满计数器"""
    _ensure()
    return _agent_pool_full


def agent_evicted():
    """取过期淘汰计数器"""
    _ensure()
    return _agent_evicted


def upstream_wait():
    """取上游排队等待直方图"""
    _ensure()
    return _upstream_wait