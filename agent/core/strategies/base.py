"""
BaseStrategy 抽象接口 + 共享的"工具循环"骨架

策略本身不直接调 LLM API 或工具，而是依赖注入进来的 provider / tool_registry，
只负责编排：多次调用 LLM、执行工具、把结果注入上下文、判断何时终止
"""

from __future__ import annotations

import asyncio
from abc import ABC, abstractmethod
from typing import AsyncIterator, List, Optional

from loguru import logger

from agent.config.loader import Config
from agent.core.types import (
    ChatMessage,
    DoneEvent,
    ErrorEvent,
    StreamEvent,
    TokenEvent,
    ToolCall,
    ToolCallEvent,
    ToolResultEvent,
)
from agent.providers.base import BaseLLMProvider, ProviderError
from agent.tools.registry import ToolRegistry


class BaseStrategy(ABC):
    """所有自主循环策略的公共接口"""

    name: str = ""  # 策略名
    description: str = ""  # 策略描述
    supports_tools: bool = True  # 是否支持工具

    @abstractmethod
    async def execute(
        self,
        provider: BaseLLMProvider,
        tool_registry: ToolRegistry,
        messages: List[ChatMessage],
        cancel_event: Optional[asyncio.Event],
        config: Config,
    ) -> AsyncIterator[StreamEvent]:
        """执行策略，产出事件流

        provider: LLM 提供商，负责实际的模型流式调用
        tool_registry: 工具注册表，策略需要时查找并执行工具
        messages: 已拼好的对话上下文，system + history + user
        cancel_event: 取消信号，一旦 set 应立即停止生成，None 表示不支持取消
        config: 全局配置，含工具开关、轮次上限等策略相关配置
        return: StreamEvent 事件流，含文本、工具调用、结果、错误与结束事件
        """

    # ------------------------------------------------------------------
    # 共享骨架：一轮 LLM + 工具调用循环
    # ------------------------------------------------------------------
    async def _tool_loop(
        self,
        provider: BaseLLMProvider,
        tool_registry: ToolRegistry,
        messages: List[ChatMessage],
        cancel_event: Optional[asyncio.Event],
        config: Config,
    ) -> AsyncIterator[StreamEvent]:
        """
        工具循环骨架：调用 provider.chat_stream → 遇 ToolCall 执行工具 → 结果注入 → 再调用，
        直到没有工具调用或达到最大轮次

        provider: LLM 提供商，负责实际的模型流式调用
        tool_registry: 工具注册表，策略需要时查找并执行工具
        messages: 已拼好的对话上下文，循环内会被追加工具调用结果
        cancel_event: 取消信号，一旦 set 立即终止循环并产出 cancelled 结束事件
        config: 全局配置，含工具开关、工具轮次上限等
        return: StreamEvent 事件流，含文本、工具调用、工具结果、错误与结束事件
        """
        tools = tool_registry.get_schemas() if config.tools.enabled else None
        working = list(messages)
        max_rounds = config.tools.max_rounds if tools else 1

        for _round in range(max_rounds):
            if cancel_event is not None and cancel_event.is_set():
                logger.debug("策略收到取消信号，终止工具循环")
                yield DoneEvent(cancelled=True, finish_reason="cancelled")
                return

            tool_calls: List[ToolCallEvent] = []
            llm_finished = False
            round_reasoning = ""  # 本轮 LLM 的思考内容，随 assistant 消息一起回传

            try:
                async for event in provider.chat_stream(working, tools, cancel_event):
                    if isinstance(event, TokenEvent):
                        yield event
                    elif isinstance(event, ToolCallEvent):
                        tool_calls.append(event)
                        # 先把 tool_call 事件透传给上层（前端可显示"正在搜索…"）
                        yield event
                    elif isinstance(event, DoneEvent):
                        llm_finished = True
                        round_reasoning = event.reasoning_content  # 记录本轮思考内容
                        # 结束事件暂不转发，等工具执行完或确认无工具后统一发
                    elif isinstance(event, ErrorEvent):
                        yield event
                        return
            except ProviderError as exc:
                yield ErrorEvent(code=exc.code, message=exc.message, recoverable=exc.recoverable)
                return

            if tool_calls:
                # 执行工具，把结果注入上下文
                for tc in tool_calls:
                    result = await tool_registry.execute_json(tc.tool_name, tc.arguments, tc.tool_call_id)
                    yield ToolResultEvent(
                        tool_call_id=tc.tool_call_id,
                        name=tc.tool_name,
                        result=result.result,
                        is_error=result.is_error,
                    )
                    # 注入 assistant 的 tool_call + tool 结果两条消息
                    working.append(
                        ChatMessage(
                            role="assistant",
                            content="",
                            reasoning_content=round_reasoning,  # 思考模式需把本轮推理带回 API
                            tool_calls=[
                                ToolCall(id=tc.tool_call_id, name=tc.tool_name, arguments=tc.arguments)
                            ],
                        )
                    )
                    working.append(
                        ChatMessage(
                            role="tool",
                            content=result.result,
                            tool_call_id=tc.tool_call_id,
                            name=tc.tool_name,
                        )
                    )
                continue  # 进入下一轮 LLM 调用

            # 没有工具调用，说明 LLM 直接给出了最终回复
            if llm_finished:
                yield DoneEvent(finish_reason="stop", reasoning_content=round_reasoning)
                return
            # 没有 DoneEvent 也没有 tool_call：异常兜底
            yield DoneEvent(finish_reason="error")
            return

        # 达到最大工具轮次仍没结束，把最后一轮思考内容带上供历史回传
        yield DoneEvent(finish_reason="tool_loop_limit", reasoning_content=round_reasoning)
