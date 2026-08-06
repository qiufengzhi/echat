"""ReAct 策略：思考 → 行动 → 观察 循环

需要查信息、调用工具的复杂问题用这个策略。基于原生 function calling：
LLM 在"推理"和"调工具"之间循环，直到给出 Final Answer
"""

from __future__ import annotations

import asyncio
from typing import AsyncIterator, List, Optional

from agent.config.loader import Config
from agent.core.strategies.base import BaseStrategy
from agent.core.strategies.simple import SimpleStrategy
from agent.core.types import ChatMessage, StreamEvent
from agent.providers.base import BaseLLMProvider
from agent.tools.registry import ToolRegistry

# ReAct 专用系统提示词
_REACT_PROMPT = (
    "你是一个会使用工具的助手。需要查实时信息、算日期、搜索网页等问题时，"
    "先调用工具获取信息，再基于工具结果给出最终回答。"
    "如果不需要工具就能回答，直接回答。"
)


class ReActStrategy(BaseStrategy):
    """ReAct 自主循环：Thought → Action → Observation → ... → Final Answer"""

    name = "react"  # 策略名
    description = "思考 → 行动 → 观察 循环，适合需要查资料的复杂问题"  # 策略描述

    async def execute(
        self,
        provider: BaseLLMProvider,
        tool_registry: ToolRegistry,
        messages: List[ChatMessage],
        cancel_event: Optional[asyncio.Event],
        config: Config,
    ) -> AsyncIterator[StreamEvent]:
        """无工具时降级为 simple，否则注入 ReAct 提示词后走工具循环

        provider: LLM 提供商，负责实际的模型流式调用
        tool_registry: 工具注册表，需要时查找并调用工具
        messages: 已拼好的对话上下文，ReAct 提示词会注入到首条 system 消息
        cancel_event: 取消信号，一旦 set 立即终止生成，None 表示不支持取消
        config: 全局配置，含工具开关等，用于判断是否降级为 simple
        return: StreamEvent 事件流，含文本、工具调用、结果与结束事件
        """
        if not config.tools.enabled or not tool_registry.list_names():
            # 没工具可用 → 降级为 simple
            simple = SimpleStrategy()
            async for event in simple.execute(
                provider, tool_registry, messages, cancel_event, config
            ):
                yield event
            return

        working = list(messages)
        # 注入 ReAct 系统提示词（如果有 system 消息，追加在它后面）
        if working and working[0].role == "system":
            working[0] = ChatMessage(
                role="system",
                content=working[0].content + "\n" + _REACT_PROMPT,
            )
        else:
            working.insert(0, ChatMessage(role="system", content=_REACT_PROMPT))

        async for event in self._tool_loop(provider, tool_registry, working, cancel_event, config):
            yield event
