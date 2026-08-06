"""
Session：一段对话的运行时状态和生命周期

注意区分：Session 是"运行时对象"（持状态机、cancel_event、历史），
对话历史的持久化存储由 memory/ 负责，Session 只是持有历史副本
"""

from __future__ import annotations

import asyncio
import time
from collections import deque
from typing import Deque, Dict, Optional

from agent.core.types import ChatMessage, SessionState


class Session:
    """单会话运行时对象"""

    def __init__(
        self,
        session_id: str,
        room_id: str = "",
        max_history: int = 12,
        ttl_seconds: float = 1800,
    ) -> None:
        """初始化会话运行时对象

        session_id: 会话唯一标识
        room_id: 会话所属房间 ID，默认空串表示不归属任何房间
        max_history: 历史消息队列最大条数，超限自动丢弃最旧消息，默认 12
        ttl_seconds: 会话过期秒数，超过该时长无活动即视为过期，默认 1800
        """
        self.session_id = session_id  # 会话 ID
        self.room_id = room_id  # 所属房间 ID
        self.state = SessionState.ACTIVE  # 会话状态
        self.created_at = time.time()  # 创建时间戳
        self.last_active_at = time.time()  # 最近活动时间戳
        self.history: Deque[ChatMessage] = deque(maxlen=max_history)  # 历史消息队列
        self.cancel_event = asyncio.Event()  # 取消信号事件
        self.metadata: Dict[str, str] = {}  # 附加元数据
        self._ttl_seconds = ttl_seconds  # 过期秒数
        self._generation_count = 0  # 当前正生成的回复数（用于并发控制）

    # ------------------------------------------------------------------
    # 状态与生命周期
    # ------------------------------------------------------------------
    def touch(self) -> None:
        """记录一次活动，刷新过期时间"""
        self.last_active_at = time.time()

    def is_expired(self, now: Optional[float] = None) -> bool:
        """是否已超过 TTL 无活动

        now: 当前时间戳（秒），缺省取系统当前时间
        return: 超过 TTL 无活动返回 True，否则 False
        """
        now = now if now is not None else time.time()
        return (now - self.last_active_at) > self._ttl_seconds

    def cancel(self) -> None:
        """发出中断信号，正在进行的 LLM 生成应尽快停止"""
        self.state = SessionState.CANCELLING
        self.cancel_event.set()

    def mark_generating(self) -> None:
        """标记进入生成状态"""
        self._generation_count += 1

    def mark_idle(self) -> None:
        """标记退出生成状态"""
        self._generation_count = max(0, self._generation_count - 1)

    @property
    def is_generating(self) -> bool:
        """是否正在生成"""
        return self._generation_count > 0

    # ------------------------------------------------------------------
    # 历史
    # ------------------------------------------------------------------
    def add_message(self, msg: ChatMessage) -> None:
        """追加一条历史消息

        msg: 要追加的聊天消息，超过 max_history 时自动丢弃最旧消息
        """
        self.history.append(msg)

    def get_messages(self) -> list:
        """取历史消息快照

        return: 按时间顺序排列的消息列表，为当前历史队列的副本
        """
        return list(self.history)

    def clear_history(self) -> None:
        """清空历史"""
        self.history.clear()
