"""
OpenAI 兼容 API 的 LLM Provider

一套代码覆盖所有 OpenAI 兼容端点：DeepSeek、OpenAI、Groq、vLLM、Ollama……
区别只在 base_url 和 model 的值。API 签名都是 /v1/chat/completions

只有当某个 LLM 不是 OpenAI 兼容格式（如 Claude 原生 SDK）时，
才需要新增一个 provider 文件
"""

from __future__ import annotations

import asyncio
import time
from typing import AsyncIterator, List, Optional

from loguru import logger
from opentelemetry import trace
from openai import AsyncOpenAI

from agent.core.types import (
    ChatMessage,
    DoneEvent,
    StreamEvent,
    TokenEvent,
    TokenUsage,
    ToolCallEvent,
)
from agent.observability import attrs
import agent.observability.metrics as obs_metrics
from agent.providers.base import (
    BaseLLMProvider,
    LLMAPIConnectionError,
    LLMAuthError,
    LLMRateLimitError,
    LLMTimeoutError,
    ProviderError,
)


def _to_openai_messages(messages: List[ChatMessage]) -> List[dict]:
    """把内部 ChatMessage 转成 OpenAI API 的消息格式

    messages: 内部结构化消息列表，含 system、user、assistant、tool 四类角色
    return: OpenAI 兼容的请求体消息列表，assistant 带 tool_calls、tool 带 tool_call_id，思考模式带 reasoning_content
    """
    out: List[dict] = []
    for m in messages:
        if m.role == "assistant" and m.tool_calls:
            msg: dict = {
                "role": "assistant",
                "content": m.content or None,
                "tool_calls": [
                    {
                        "id": tc.id,
                        "type": "function",
                        "function": {"name": tc.name, "arguments": tc.arguments},
                    }
                    for tc in m.tool_calls
                ],
            }
        elif m.role == "tool":
            msg = {
                "role": "tool",
                "content": m.content,
                "tool_call_id": m.tool_call_id or "",
            }
        else:
            msg = {"role": m.role, "content": m.content}
        # 思考模式要求 assistant 消息带上上一轮返回的 reasoning_content，否则 API 400
        if m.role == "assistant" and m.reasoning_content:
            msg["reasoning_content"] = m.reasoning_content
        out.append(msg)
    return out


class OpenAICompatibleProvider(BaseLLMProvider):
    """通用 OpenAI 兼容 provider，基于异步客户端实现流式生成"""

    def __init__(
        self,
        api_key: str,
        base_url: str,
        model: str,
        provider_name: str = "openai-compatible",
        max_tokens: int = 1024,
        temperature: float = 0.7,
        request_timeout: float = 30.0,
        max_concurrency: int = 8,
        first_token_timeout: float = 5.0,
    ) -> None:
        """初始化 OpenAI 兼容 provider

        api_key: 服务商 API 密钥
        base_url: OpenAI 兼容接口地址，如 https://api.deepseek.com/v1
        model: 模型名，如 deepseek-chat
        provider_name: 提供商名，写入 gen_ai.provider.name，默认 openai-compatible
        max_tokens: 单次回复最大 token 数，默认 1024
        temperature: 采样温度，越高越随机，默认 0.7
        request_timeout: 单次请求超时秒数，默认 30
        max_concurrency: 同时打到上游的最大请求数，默认 8，超出的轮次在本地排队
        first_token_timeout: 首包超时秒数，默认 5，超时按可重试错误结束本轮
        """
        self._model = model  # 模型名
        self._provider_name = provider_name  # 提供商名
        self._max_tokens = max_tokens  # 单次回复最大 token 数
        self._temperature = temperature  # 采样温度
        self._request_timeout = request_timeout  # 请求超时秒数
        self._first_token_timeout = first_token_timeout  # 首包超时秒数
        self._semaphore = asyncio.Semaphore(max(1, max_concurrency))  # 上游并发闸门
        self._client = AsyncOpenAI(api_key=api_key, base_url=base_url, timeout=request_timeout)  # 异步客户端
        self._logger = logger.bind(module="provider")  # 模块级日志器

    @property
    def model_name(self) -> str:
        """当前模型名"""
        return self._model

    @property
    def token_limit(self) -> int:
        """上下文窗口大小"""
        # DeepSeek V3 上下文 64K；通用 OpenAI 兼容模型默认给 128K
        return 131072

    def supports_tools(self) -> bool:
        """是否支持 function calling"""
        # OpenAI 兼容端点基本都支持 function calling
        return True

    async def chat_stream(
        self,
        messages: List[ChatMessage],
        tools: Optional[List[dict]] = None,
        cancel_event: Optional[asyncio.Event] = None,
    ) -> AsyncIterator[StreamEvent]:
        """流式调用 OpenAI 兼容端点，产出 TokenEvent / ToolCallEvent / DoneEvent

        并发闸门在此生效：同时打到上游的请求数不超过 max_concurrency，超出的轮次在信号量上排队
        messages: 对话上下文，内部转成 OpenAI 消息格式
        tools: OpenAI function calling schema 列表，None 表示本轮不提供工具
        cancel_event: 取消信号，一旦 set 立即终止流并产出 cancelled 结束事件
        return: StreamEvent 事件流，含文本、聚合后的工具调用与用量统计
        调用失败时抛出 ProviderError 子类异常
        """
        if self._semaphore.locked():
            self._logger.info("上游并发已打满，本轮排队等待")
        async with self._semaphore:
            async for event in self._stream_once(messages, tools, cancel_event):
                yield event

    async def _stream_once(
        self,
        messages: List[ChatMessage],
        tools: Optional[List[dict]] = None,
        cancel_event: Optional[asyncio.Event] = None,
    ) -> AsyncIterator[StreamEvent]:
        """单次流式调用：建流、逐分片产出、首包超时、退出时释放上游连接

        messages: 对话上下文
        tools: 工具 schema 列表，None 表示不提供工具
        cancel_event: 取消信号，一旦 set 立即终止流
        return: StreamEvent 事件流
        """
        kwargs: dict = {
            "model": self._model,
            "messages": _to_openai_messages(messages),
            "max_tokens": self._max_tokens,
            "temperature": self._temperature,
            "stream": True,
        }
        if tools:
            kwargs["tools"] = tools

        # LLM 请求 span：gen_ai.* 语义约定属性供后端按名解析（不写消息内容，见 PII 规则）
        tracer = trace.get_tracer("echat-agent")
        span = tracer.start_span(
            "agent.llm.request",
            attributes={
                attrs.ATTR_GEN_AI_REQUEST_MODEL: self._model,
                attrs.ATTR_GEN_AI_PROVIDER_NAME: self._provider_name,
                attrs.ATTR_GEN_AI_REQUEST_MAX_TOKENS: self._max_tokens,
                attrs.ATTR_GEN_AI_REQUEST_TEMPERATURE: self._temperature,
                attrs.ATTR_GEN_AI_REQUEST_STREAM: True,
            },
        )
        t_start = time.perf_counter()
        span_attrs = {
            attrs.ATTR_GEN_AI_REQUEST_MODEL: self._model,
            attrs.ATTR_GEN_AI_PROVIDER_NAME: self._provider_name,
        }

        try:
            stream = await self._client.chat.completions.create(**kwargs)
        except Exception as exc:
            mapped = self._map_error(exc)
            self._logger.warning("LLM 创建流失败: {}", mapped.message)
            raise mapped

        tool_calls_accum: dict = {}  # 按 index 聚合分片的 tool_call
        thinking_parts: list = []  # 思考模式的推理片段，按序聚合
        prompt_tokens = 0  # 输入 token 数
        completion_tokens = 0  # 输出 token 数
        finish_reason = "stop"  # 结束原因

        iterator = stream.__aiter__()  # 显式取迭代器：首个分片要单独套首包超时
        first_chunk = True
        try:
            while True:
                try:
                    if first_chunk:
                        chunk = await asyncio.wait_for(
                            iterator.__anext__(), timeout=self._first_token_timeout
                        )
                        first_chunk = False
                        ttfc_s = time.perf_counter() - t_start
                        obs_metrics.llm_ttft().record(ttfc_s * 1000)
                        obs_metrics.ttfc().record(ttfc_s, attributes=span_attrs)
                        span.set_attribute("gen_ai.response.time_to_first_chunk", ttfc_s)
                        span.add_event("llm.first_token")
                    else:
                        chunk = await iterator.__anext__()
                except StopAsyncIteration:
                    break
                except asyncio.TimeoutError:
                    self._logger.warning("上游首包超时({}s)", self._first_token_timeout)
                    raise LLMTimeoutError(
                        f"上游超过 {self._first_token_timeout:.0f} 秒没有响应，等会儿再问我一次吧"
                    )
                if cancel_event is not None and cancel_event.is_set():
                    self._logger.debug("收到 cancel 信号，终止 LLM 流")
                    finish_reason = "cancelled"
                    break

                if not chunk.choices:
                    continue
                choice = chunk.choices[0]

                if getattr(choice, "finish_reason", None):
                    finish_reason = choice.finish_reason or finish_reason

                delta = choice.delta
                if delta is None:
                    continue

                if delta.content:
                    yield TokenEvent(content=delta.content)

                # 思考模式模型在正文前逐片返回 reasoning_content，需收集用于下一轮回传
                if getattr(delta, "reasoning_content", None):
                    thinking_parts.append(delta.reasoning_content)

                if delta.tool_calls:
                    for tc in delta.tool_calls:
                        idx = tc.index
                        acc = tool_calls_accum.setdefault(
                            idx, {"id": "", "name": "", "arguments": ""}
                        )
                        if tc.id:
                            acc["id"] = tc.id
                        if tc.function and tc.function.name:
                            acc["name"] += tc.function.name
                        if tc.function and tc.function.arguments:
                            acc["arguments"] += tc.function.arguments

                if chunk.usage:
                    prompt_tokens = chunk.usage.prompt_tokens or 0
                    completion_tokens = chunk.usage.completion_tokens or 0

            # 所有 tool_call 都聚齐后逐个产出
            for idx in sorted(tool_calls_accum.keys()):
                acc = tool_calls_accum[idx]
                if acc["name"]:
                    yield ToolCallEvent(
                        tool_call_id=acc["id"] or f"call_{idx}",
                        tool_name=acc["name"],
                        arguments=acc["arguments"],
                    )

            # token 用量与请求耗时：gen_ai.* 语义约定名，label 仅限有限枚举
            obs_metrics.tokens().record(
                prompt_tokens,
                attributes={**span_attrs, "gen_ai.token.type": "input"},
            )
            obs_metrics.tokens().record(
                completion_tokens,
                attributes={**span_attrs, "gen_ai.token.type": "output"},
            )
            obs_metrics.dur().record(time.perf_counter() - t_start, attributes=span_attrs)
            span.set_attribute(attrs.ATTR_GEN_AI_USAGE_INPUT_TOKENS, prompt_tokens)
            span.set_attribute(attrs.ATTR_GEN_AI_USAGE_OUTPUT_TOKENS, completion_tokens)
            span.set_attribute(attrs.ATTR_GEN_AI_RESPONSE_FINISH_REASONS, (finish_reason,))

            yield DoneEvent(
                cancelled=(finish_reason == "cancelled"),
                finish_reason=finish_reason,
                reasoning_content="".join(thinking_parts),
                usage=TokenUsage(
                    prompt_tokens=prompt_tokens,
                    completion_tokens=completion_tokens,
                    total_tokens=prompt_tokens + completion_tokens,
                ),
            )

        except ProviderError:
            # 首包超时等本层已结构化的错误，不再二次映射，保留原始错误码
            span.set_status(trace.Status(trace.StatusCode.ERROR))
            raise
        except Exception as exc:
            mapped = self._map_error(exc)
            span.set_status(trace.Status(trace.StatusCode.ERROR))
            self._logger.warning("LLM 流式调用失败: {}", mapped.message)
            raise mapped
        finally:
            span.end()
            # 上游连接必须显式关闭：取消、超时、正常结束都不该让连接与计费继续
            try:
                await stream.close()
            except Exception:
                self._logger.debug("关闭上游流失败")

    def _map_error(self, exc: Exception) -> Exception:
        """把 openai SDK 异常映射成 ProviderError 子类

        exc: openai SDK 抛出的异常
        return: 对应类型的 ProviderError 子类，未分类异常归为连接错误
        """
        import openai

        if isinstance(exc, openai.AuthenticationError):
            return LLMAuthError()
        if isinstance(exc, openai.RateLimitError):
            return LLMRateLimitError()
        if isinstance(exc, openai.APITimeoutError):
            return LLMTimeoutError()
        if isinstance(exc, (openai.APIConnectionError, openai.APIStatusError, openai.APIError)):
            return LLMAPIConnectionError(str(exc))
        # 其它未分类异常
        return LLMAPIConnectionError(str(exc))
