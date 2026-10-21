"""
真实上游冒烟配额守卫（docs/loadtest-task-prompt.md 3.2 成本红线的强制执行层）

机制（不依赖人工自觉）：
  1. 先记账后调用：每次真实 LLM 调用前消耗 1 个计数并立即原子落盘，
     进程崩溃也不超支（记账在调用前完成）；
  2. 超限自动降级：计数达到上限后，后续调用自动走 fallback（mock provider），
     并打 WARN 日志，压测流程不中断；
  3. 计数持久化：JSON 落盘，进程重启不重置，防止配额失控；
  4. 单轮一次调用计数：一轮 Generate 消耗 1 次配额，不按 token 复算。

注意：ASR/TTS 配额在 Go backend 侧（asr_cli/tts_cli），由后续 backend mock 钩子
commit 落地；本类只覆盖 agent 侧的 LLM 配额。
"""

from __future__ import annotations

import json
import os
from typing import AsyncIterator, List, Optional

from loguru import logger

from agent.core.types import ChatMessage, StreamEvent
from agent.providers.base import BaseLLMProvider


class QuotaProvider(BaseLLMProvider):
    """带持久化配额上限的 provider 包装：真实调用受控，超限自动降级 mock"""

    def __init__(
        self,
        real: BaseLLMProvider,
        fallback: BaseLLMProvider,
        counter_path: str,
        limit: int,
    ) -> None:
        """初始化配额守卫

        real: 真实 provider（如 OpenAICompatibleProvider/DeepSeek）
        fallback: 配额耗尽后的降级 provider（MockProvider）
        counter_path: 配额计数 JSON 持久化路径（进程重启不重置）
        limit: 真实调用硬上限（次）
        """
        self._real = real  # 真实 provider
        self._fallback = fallback  # 降级 provider
        self._counter_path = counter_path  # 计数持久化路径
        self._limit = limit  # 硬上限
        self._used = self._load()  # 已消耗计数（从盘上恢复）
        self._degraded = False  # 超限降级开关，置位后不再读盘
        logger.info(
            "LLM 真实冒烟配额已启用: {}/{}，计数文件: {}", self._used, self._limit, self._counter_path
        )

    # ------------------------------------------------------------------
    # 配额记账（事件循环内同步执行，无 await，天然串行安全）
    # ------------------------------------------------------------------
    def _load(self) -> int:
        """从持久化文件恢复已消耗计数；文件缺失或损坏按 0 处理"""
        try:
            with open(self._counter_path, "r", encoding="utf-8") as f:
                return int(json.load(f).get("llm_calls", 0))
        except (OSError, ValueError):
            return 0

    def _save(self) -> None:
        """原子落盘当前计数（写临时文件后 replace，防中途崩溃写坏文件）"""
        os.makedirs(os.path.dirname(self._counter_path) or ".", exist_ok=True)
        tmp = self._counter_path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump({"llm_calls": self._used, "limit": self._limit}, f)
        os.replace(tmp, self._counter_path)

    def _acquire(self) -> bool:
        """先记账后调用；返回 False 表示配额耗尽需降级"""
        if self._degraded or self._used >= self._limit:
            self._degraded = True
            return False
        self._used += 1
        self._save()
        return True

    # ------------------------------------------------------------------
    # BaseLLMProvider 接口：元信息透传真实 provider
    # ------------------------------------------------------------------
    @property
    def model_name(self) -> str:
        """透传真实 provider 的模型名"""
        return self._real.model_name

    @property
    def token_limit(self) -> int:
        """透传真实 provider 的上下文窗口"""
        return self._real.token_limit

    def supports_tools(self) -> bool:
        """透传真实 provider 的工具支持"""
        return self._real.supports_tools()

    async def chat_stream(
        self,
        messages: List[ChatMessage],
        tools: Optional[List[dict]] = None,
        cancel_event: Optional[object] = None,
    ) -> AsyncIterator[StreamEvent]:
        """配额内走真实调用，超限自动降级 mock

        messages/tools/cancel_event: 语义同 BaseLLMProvider.chat_stream
        return: 真实或降级 provider 的事件流
        """
        if not self._acquire():
            logger.warning(
                "LLM 真实冒烟配额已达上限（{}/{}），本轮自动降级为 mock",
                self._used, self._limit,
            )
            async for ev in self._fallback.chat_stream(messages, tools, cancel_event):
                yield ev
            return
        logger.info("LLM 真实冒烟配额消耗: {}/{}", self._used, self._limit)
        async for ev in self._real.chat_stream(messages, tools, cancel_event):
            yield ev
