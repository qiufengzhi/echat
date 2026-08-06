"""内存实现：完全复现"重启后数据丢失"的行为，是默认实现"""

from __future__ import annotations

import threading
from typing import Dict, List, Optional

from agent.core.types import ChatMessage
from agent.memory.base import BaseMemory


class InMemoryMemory(BaseMemory):
    """基于 dict 的内存存储，进程内共享"""

    def __init__(self) -> None:
        """初始化内存存储，进程内共享"""
        self._store: Dict[str, List[ChatMessage]] = {}  # session_id → 消息列表
        self._lock = threading.Lock()  # 并发访问锁

    def load(self, session_id: str) -> Optional[List[ChatMessage]]:
        """加载会话历史，不存在返回 None

        session_id: 要加载的会话标识
        return: 会话历史消息列表副本，从未保存时返回 None
        """
        with self._lock:
            msgs = self._store.get(session_id)
            return list(msgs) if msgs is not None else None

    def save(self, session_id: str, messages: List[ChatMessage]) -> None:
        """保存会话历史

        session_id: 要保存的会话标识，已存在则覆盖
        messages: 要持久化的历史消息列表，保存为副本
        """
        with self._lock:
            self._store[session_id] = list(messages)

    def delete(self, session_id: str) -> None:
        """删除会话历史

        session_id: 要删除的会话标识，不存在时静默忽略
        """
        with self._lock:
            self._store.pop(session_id, None)

    def list_sessions(self, limit: int = 100, offset: int = 0) -> List[str]:
        """列出所有已存 session_id

        limit: 最多返回的会话数，默认 100
        offset: 分页起始偏移，默认 0
        return: 会话标识列表，按写入顺序排列
        """
        with self._lock:
            return list(self._store.keys())[offset : offset + limit]
