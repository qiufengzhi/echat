"""
BaseMemory 抽象接口：把对话历史从 Session 对象中分离出来

存储方式由子类决定（内存 dict / SQLite / Redis），对上层暴露统一接口
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import List, Optional

from agent.core.types import ChatMessage


class BaseMemory(ABC):
    """对话历史存储接口"""

    @abstractmethod
    def load(self, session_id: str) -> Optional[List[ChatMessage]]:
        """加载会话历史，不存在返回 None

        session_id: 要加载的会话标识
        return: 会话历史消息列表，该会话从未保存时返回 None
        """

    @abstractmethod
    def save(self, session_id: str, messages: List[ChatMessage]) -> None:
        """保存会话历史

        session_id: 要保存的会话标识，已存在则覆盖
        messages: 要持久化的历史消息列表
        """

    @abstractmethod
    def delete(self, session_id: str) -> None:
        """删除会话历史

        session_id: 要删除的会话标识，不存在时静默忽略
        """

    @abstractmethod
    def list_sessions(self, limit: int = 100, offset: int = 0) -> List[str]:
        """列出所有已存 session_id

        limit: 最多返回的会话数，默认 100
        offset: 分页起始偏移，默认 0
        return: 会话标识列表，按各实现的排序规则排列
        """
