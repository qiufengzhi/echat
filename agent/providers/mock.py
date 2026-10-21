"""
Mock LLM Provider：压测与本地开发用的确定性假实现

设计目标：时序形态对齐真实语音链路——先 TTFT（首包前延迟），再按节奏吐 token，
使会话层压测的端到端耗时曲线具备解释性（真实链路校准见 docs/loadtest.md §1.6 B4）。
默认节奏：TTFT 300ms + 每 token 30ms × 20 段 ≈ 0.9s 单轮回复，与真实短轮次同量级。
"""

from __future__ import annotations

import asyncio
from typing import AsyncIterator, List, Optional

from agent.core.types import ChatMessage, DoneEvent, TokenEvent
from agent.providers.base import BaseLLMProvider


class MockProvider(BaseLLMProvider):
    """按固定节奏产出回复的 mock provider，不发起任何真实上游调用"""

    model_name = "mock-model"  # 当前模型名
    token_limit = 4096  # 上下文窗口大小

    def supports_tools(self) -> bool:
        """mock 不支持 function calling，orchestrator 会跳过工具注入"""
        return False

    def __init__(
        self,
        first_token_delay: float = 0.3,
        token_interval: float = 0.03,
        reply: str = "好的，我在听，你继续说说看，这个问题其实挺有意思的。",
        chunk: int = 2,
    ) -> None:
        """初始化 mock provider

        first_token_delay: 首包前延迟秒数（模拟 LLM TTFT）
        token_interval: 每段输出间隔秒数（模拟流式吐字节奏）
        reply: 固定回复文本，按 chunk 切成多段产出
        chunk: 每段输出的字符数
        """
        self.first_token_delay = first_token_delay  # 首包前延迟
        self.token_interval = token_interval  # 段间延迟
        self.reply = reply  # 固定回复文本
        self.chunk = chunk  # 每段字符数
        self.calls = 0  # 已调用次数（观测用）
        self.cancelled = 0  # 被取消次数（观测用）

    async def chat_stream(
        self,
        messages: List[ChatMessage],
        tools: Optional[List[dict]] = None,
        cancel_event: Optional[object] = None,
    ) -> AsyncIterator[StreamEvent]:
        """按固定节奏产出 token 事件流，响应取消信号

        messages: 对话上下文，mock 不解析仅计数
        tools: 工具 schema，mock 不支持，忽略
        cancel_event: 取消信号（threading.Event 同款 is_set 接口），set 后立即终止
        return: TokenEvent* + DoneEvent 的事件流
        """
        self.calls += 1
        await asyncio.sleep(self.first_token_delay)
        for i in range(0, len(self.reply), self.chunk):
            if cancel_event is not None and cancel_event.is_set():
                self.cancelled += 1
                yield DoneEvent(cancelled=True, finish_reason="cancelled")
                return
            yield TokenEvent(content=self.reply[i : i + self.chunk])
            await asyncio.sleep(self.token_interval)
        yield DoneEvent(finish_reason="stop")
