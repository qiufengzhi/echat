"""
OpenAI 兼容 API 的 LLM Provider

一套代码覆盖所有 OpenAI 兼容端点：DeepSeek、OpenAI、Groq、vLLM、Ollama……
区别只在 base_url 和 model 的值。API 签名都是 /v1/chat/completions

只有当某个 LLM 不是 OpenAI 兼容格式（如 Claude 原生 SDK）时，
才需要新增一个 provider 文件
"""

from __future__ import annotations

import asyncio
from typing import AsyncIterator, List, Optional

from loguru import logger
from openai import AsyncOpenAI

from agent.core.types import (
    ChatMessage,
    DoneEvent,
    StreamEvent,
    TokenEvent,
    TokenUsage,
    ToolCallEvent,
)
from agent.providers.base import (
    BaseLLMProvider,
    LLMAPIConnectionError,
    LLMAuthError,
    LLMRateLimitError,
    LLMTimeoutError,
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
        max_tokens: int = 1024,
        temperature: float = 0.7,
        request_timeout: float = 30.0,
    ) -> None:
        """初始化 OpenAI 兼容 provider

        api_key: 服务商 API 密钥
        base_url: OpenAI 兼容接口地址，如 https://api.deepseek.com/v1
        model: 模型名，如 deepseek-chat
        max_tokens: 单次回复最大 token 数，默认 1024
        temperature: 采样温度，越高越随机，默认 0.7
        request_timeout: 单次请求超时秒数，默认 30
        """
        self._model = model  # 模型名
        self._max_tokens = max_tokens  # 单次回复最大 token 数
        self._temperature = temperature  # 采样温度
        self._request_timeout = request_timeout  # 请求超时秒数
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

        messages: 对话上下文，内部转成 OpenAI 消息格式
        tools: OpenAI function calling schema 列表，None 表示本轮不提供工具
        cancel_event: 取消信号，一旦 set 立即终止流并产出 cancelled 结束事件
        return: StreamEvent 事件流，含文本、聚合后的工具调用与用量统计
        调用失败时抛出 ProviderError 子类异常
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

        try:
            async for chunk in stream:
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

        except Exception as exc:
            mapped = self._map_error(exc)
            self._logger.warning("LLM 流式调用失败: {}", mapped.message)
            raise mapped

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
