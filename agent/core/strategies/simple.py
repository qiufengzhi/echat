"""Simple 策略：一问一答（当前代码的行为）"""

from __future__ import annotations

import asyncio
from typing import AsyncIterator, List, Optional

from agent.config.loader import Config
from agent.core.strategies.base import BaseStrategy
from agent.core.types import ChatMessage, StreamEvent
from agent.providers.base import BaseLLMProvider
from agent.tools.registry import ToolRegistry


class SimpleStrategy(BaseStrategy):
    """简单模式：收到消息 → 调 LLM → 回复"""

    name = "simple"  # 策略名
    description = "一问一答，不主动规划"  # 策略描述

    async def execute(
        self,
        provider: BaseLLMProvider,
        tool_registry: ToolRegistry,
        messages: List[ChatMessage],
        cancel_event: Optional[asyncio.Event],
        config: Config,
    ) -> AsyncIterator[StreamEvent]:
        """直接走共享的工具循环，LLM 决定是否调工具

        provider: LLM 提供商，负责实际的模型流式调用
        tool_registry: 工具注册表，LLM 要求调用工具时查找并执行
        messages: 已拼好的对话上下文，原样传入工具循环
        cancel_event: 取消信号，一旦 set 立即终止生成，None 表示不支持取消
        config: 全局配置，含工具开关、轮次上限等
        return: StreamEvent 事件流，含文本、工具调用、结果与结束事件
        """
        async for event in self._tool_loop(provider, tool_registry, messages, cancel_event, config):
            yield event
