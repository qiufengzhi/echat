"""
ChatOrchestrator：Agent 的大脑

它不调 LLM API、不写 proto、不操作存储，只做编排：
  - 管理会话池（创建 / 淘汰 / 上限）
  - 从 memory 恢复 / 保存历史
  - 选择策略（simple / react / plan_and_solve）
  - 处理中断
  - 聚合健康状态
"""

from __future__ import annotations

import asyncio
import re
import time
from typing import AsyncIterator, Dict, Optional

from loguru import logger

from agent.config.loader import Config
from agent.core.session import Session
from agent.core.strategies import PlanAndSolveStrategy, ReActStrategy, SimpleStrategy
from agent.core.types import (
    ChatMessage,
    DoneEvent,
    ErrorEvent,
    HealthStatus,
    StreamEvent,
    TokenEvent,
)
from agent.memory.base import BaseMemory
from agent.providers.base import BaseLLMProvider
from agent.tools.registry import ToolRegistry


class ChatOrchestrator:
    """编排引擎：把 LLM、工具、会话、记忆串起来"""

    def __init__(
        self,
        provider: BaseLLMProvider,
        tool_registry: ToolRegistry,
        memory: BaseMemory,
        config: Config,
    ) -> None:
        """初始化编排引擎，注入 LLM、工具、记忆与配置

        provider: LLM 提供商，负责与后端模型通信
        tool_registry: 工具注册表，策略执行时查找并调用工具
        memory: 记忆存储，负责会话历史的持久化读写
        config: 全局配置，含会话、工具、策略等各段配置
        """
        self._provider = provider  # LLM 提供商
        self._tool_registry = tool_registry  # 工具注册表
        self._memory = memory  # 记忆存储
        self._config = config  # 全局配置
        self._sessions: Dict[str, Session] = {}  # 会话池
        self._pool_lock = asyncio.Lock()  # 会话池并发锁
        self._start_time = time.time()  # 服务启动时间戳
        self._last_provider_error: Optional[str] = None  # 最近一次 provider 错误码
        self._strategy_cache = {  # 策略名到策略类的映射
            "simple": SimpleStrategy,
            "react": ReActStrategy,
            "plan_and_solve": PlanAndSolveStrategy,
        }

    # ------------------------------------------------------------------
    # 会话管理
    # ------------------------------------------------------------------
    async def get_or_create_session(self, session_id: str, room_id: str = "") -> Session:
        """按 ID 取会话，不存在则创建并尝试从记忆恢复

        session_id: 会话唯一标识，为空时不允许创建
        room_id: 会话所属房间 ID，默认空串表示不归属任何房间
        return: 已存在或新建的会话对象，同时刷新其活动时间
        """
        async with self._pool_lock:
            session = self._sessions.get(session_id)
            if session is None:
                if len(self._sessions) >= self._config.session.max_sessions:
                    # 池满：淘汰最久未活动的会话
                    await self._evict_expired_locked()
                if len(self._sessions) >= self._config.session.max_sessions:
                    raise RuntimeError("SESSION_POOL_FULL")
                logger.info("创建新会话 session={} room={}", session_id, room_id)
                session = Session(
                    session_id=session_id,
                    room_id=room_id,
                    max_history=self._config.session.max_history,
                    ttl_seconds=self._config.session.ttl_seconds,
                )
                self._sessions[session_id] = session
                # 从持久化记忆恢复历史
                try:
                    restored = self._memory.load(session_id)
                    if restored:
                        for msg in restored:
                            session.add_message(msg)
                        logger.debug("会话 {} 从记忆恢复 {} 条历史", session_id, len(restored))
                except Exception:
                    logger.exception("恢复会话历史失败 session={}", session_id)
            session.touch()
            return session

    async def cancel(self, session_id: str) -> None:
        """中断指定会话正在进行的 LLM 生成

        session_id: 要中断的会话标识
        """
        async with self._pool_lock:
            session = self._sessions.get(session_id)
        if session is not None:
            session.cancel()
            logger.info("已请求取消会话 {} 的生成", session_id)

    async def _evict_expired_sessions(self) -> None:
        """惰性淘汰所有过期会话"""
        async with self._pool_lock:
            await self._evict_expired_locked()

    async def _evict_expired_locked(self) -> None:
        """持有锁时淘汰过期会话，淘汰前保存历史"""
        now = time.time()
        expired = [sid for sid, s in self._sessions.items() if s.is_expired(now)]
        for sid in expired:
            session = self._sessions.pop(sid, None)
            if session is None:
                continue
            # 淘汰前尝试保存历史
            try:
                if session.history:
                    self._memory.save(sid, session.get_messages())
            except Exception:
                logger.exception("淘汰会话时保存历史失败 session={}", sid)
            logger.info("淘汰过期会话 session={}", sid)

    # ------------------------------------------------------------------
    # 核心入口
    # ------------------------------------------------------------------
    async def handle_message(
        self,
        session_id: str,
        user_text: str,
        room_id: str = "",
    ) -> AsyncIterator[StreamEvent]:
        """接收用户文本，选择策略，产出事件流

        session_id: 会话标识，不存在则自动创建
        user_text: 用户输入文本，作为本轮 user 消息
        room_id: 会话所属房间 ID，仅首次创建会话时生效
        return: StreamEvent 异步事件流，含文本、工具调用、结果、错误与结束事件
        """

        session = await self.get_or_create_session(session_id, room_id)
        # 惰性淘汰过期会话
        await self._evict_expired_sessions()

        # 拼装 messages = [system] + history + [user]
        messages: list = [
            ChatMessage(role="system", content=self._config.llm.system_prompt),
        ]
        messages.extend(session.get_messages())
        messages.append(ChatMessage(role="user", content=user_text))

        strategy_cls = self._strategy_cache.get(
            self._config.strategy, SimpleStrategy
        )
        strategy = strategy_cls()

        # 收集 assistant 完整回复（供写回 history）
        reply_parts: list = []
        assistant_reasoning = ""  # 最后一轮 LLM 的思考内容，随历史一起写回
        cancelled = False

        session.mark_generating()
        try:
            async for event in strategy.execute(
                provider=self._provider,
                tool_registry=self._tool_registry,
                messages=messages,
                cancel_event=session.cancel_event,
                config=self._config,
            ):
                # 聚合最终回复的文本 token
                if isinstance(event, TokenEvent):
                    reply_parts.append(event.content)
                if isinstance(event, DoneEvent):
                    cancelled = event.cancelled
                    assistant_reasoning = event.reasoning_content  # 记录思考内容供历史回传

                yield event

                # 记住 provider 最近一次是否出错（供健康检查）
                if isinstance(event, ErrorEvent):
                    self._last_provider_error = event.code
                if isinstance(event, DoneEvent) and not event.cancelled:
                    self._last_provider_error = None
        finally:
            session.mark_idle()

        # 写回历史：user 消息 + assistant 完整回复
        session.add_message(ChatMessage(role="user", content=user_text))
        reply_text = "".join(reply_parts).strip()
        # 中断的半句回复不写入历史；工具中间消息也不写
        if reply_text and not cancelled:
            session.add_message(
                ChatMessage(
                    role="assistant",
                    content=reply_text,
                    reasoning_content=assistant_reasoning,
                )
            )

        # 异步保存记忆，不阻塞回复流
        try:
            loop = asyncio.get_running_loop()
            loop.create_task(self._save_memory(session))
        except Exception:
            logger.exception("调度异步保存记忆失败")

    async def _save_memory(self, session: Session) -> None:
        """在线程池里保存会话记忆，不阻塞事件循环

        session: 待保存的会话对象，取其 session_id 与历史消息写入记忆
        """
        try:
            loop = asyncio.get_running_loop()
            await loop.run_in_executor(
                None, lambda: self._memory.save(session.session_id, session.get_messages())
            )
            logger.debug("会话 {} 记忆已保存 {} 条", session.session_id, len(session.history))
        except Exception:
            logger.exception("保存记忆失败 session={}", session.session_id)

    # ------------------------------------------------------------------
    # 健康检查
    # ------------------------------------------------------------------
    async def health(self) -> HealthStatus:
        """聚合健康状态"""
        active = len(self._sessions)
        if self._last_provider_error:
            status = "DEGRADED"
        else:
            status = "SERVING"
        return HealthStatus(
            status=status,
            active_sessions=active,
            provider_name=self._provider.model_name,
            uptime_seconds=time.time() - self._start_time,
            last_error=self._last_provider_error,
        )

    @property
    def active_session_count(self) -> int:
        """当前活跃会话数"""
        return len(self._sessions)
