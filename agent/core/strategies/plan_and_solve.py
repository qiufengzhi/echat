"""Plan-and-Solve 策略：先全局规划，再逐步执行，最后汇总回答

适合目标明确、步骤可预期的复杂任务（如"安排周末行程：先查天气，再找景点，
最后推荐路线"）。比 ReAct 多了一个"先规划"的阶段
"""

from __future__ import annotations

import asyncio
from typing import AsyncIterator, List, Optional

from agent.config.loader import Config
from agent.core.strategies.base import BaseStrategy
from agent.core.strategies.simple import SimpleStrategy
from agent.core.types import (
    ChatMessage,
    DoneEvent,
    StreamEvent,
    TokenEvent,
)
from agent.providers.base import BaseLLMProvider
from agent.tools.registry import ToolRegistry

# 规划阶段系统提示词
_PLAN_PROMPT = (
    "把任务拆解成 1-3 个可以逐个执行的步骤，每个步骤输出一行。"
    "输出格式：直接列出步骤，不要序号、不要标题，每行一个步骤。"
    "如果任务简单到不需要拆解，直接输出：无需拆解"
)

# 执行阶段系统提示词
_EXEC_PROMPT = (
    "请执行上面的任务。需要调用工具时直接调用工具获取信息，"
    "然后基于工具结果完成任务。"
)


class PlanAndSolveStrategy(BaseStrategy):
    """Plan-and-Solve：规划 → 执行 → 汇总"""

    name = "plan_and_solve"  # 策略名
    description = "先规划再执行，适合多步骤复杂任务"  # 策略描述

    async def execute(
        self,
        provider: BaseLLMProvider,
        tool_registry: ToolRegistry,
        messages: List[ChatMessage],
        cancel_event: Optional[asyncio.Event],
        config: Config,
    ) -> AsyncIterator[StreamEvent]:
        """无工具时降级为 simple，否则先规划再执行

        provider: LLM 提供商，负责实际的模型流式调用
        tool_registry: 工具注册表，执行阶段需要时查找并调用工具
        messages: 已拼好的对话上下文，规划与执行两阶段共用
        cancel_event: 取消信号，一旦 set 立即终止生成，None 表示不支持取消
        config: 全局配置，含工具开关等，用于判断是否降级为 simple
        return: StreamEvent 事件流，含规划文本、执行结果与结束事件
        """
        if not config.tools.enabled or not tool_registry.list_names():
            simple = SimpleStrategy()
            async for event in simple.execute(
                provider, tool_registry, messages, cancel_event, config
            ):
                yield event
            return

        # ---- Phase 1：规划（无工具，纯 LLM）----
        plan_msgs = list(messages)
        if plan_msgs and plan_msgs[0].role == "system":
            plan_msgs[0] = ChatMessage(
                role="system", content=plan_msgs[0].content + "\n" + _PLAN_PROMPT
            )
        else:
            plan_msgs.insert(0, ChatMessage(role="system", content=_PLAN_PROMPT))

        plan_text = ""
        try:
            async for event in provider.chat_stream(plan_msgs, None, cancel_event):
                if isinstance(event, TokenEvent):
                    plan_text += event.content
                    yield event  # 透传给前端，展示"正在规划…"
                elif isinstance(event, (DoneEvent,)):
                    pass
        except Exception as exc:
            from agent.providers.base import ProviderError
            if isinstance(exc, ProviderError):
                yield DoneEvent(finish_reason="error")
                return

        steps = self._parse_steps(plan_text)
        if not steps:
            # 不需要拆解，直接进入执行阶段回答
            steps = ["完成该任务"]

        # ---- Phase 2：逐步骤执行（走工具循环）----
        exec_msgs = list(messages)
        exec_msgs.append(
            ChatMessage(role="user", content="请完成以下步骤：" + "；".join(steps))
        )
        async for event in self._tool_loop(
            provider, tool_registry, exec_msgs, cancel_event, config
        ):
            yield event

    def _parse_steps(self, plan_text: str) -> List[str]:
        """从规划文本中解析出步骤列表

        plan_text: LLM 产出的规划文本，按行拆分，含序号或「无需拆解」标记
        return: 清洗后的步骤列表，最多 8 步；无需拆解或文本为空时返回空列表
        """
        plan_text = (plan_text or "").strip()
        if not plan_text or "无需拆解" in plan_text:
            return []
        lines = [
            ln.strip().lstrip("0123456789.-、 ）)").strip()
            for ln in plan_text.splitlines()
            if ln.strip()
        ]
        return [ln for ln in lines if ln][:8]
