"""SQLite 实现：轻量持久化，服务重启后对话仍在

用单张表存 JSON 序列化的消息列表，标准库 sqlite3，零第三方依赖
适合单机部署场景
"""

from __future__ import annotations

import json
import sqlite3
import threading
import time
from pathlib import Path
from typing import List, Optional

from agent.core.types import ChatMessage, ToolCall
from agent.memory.base import BaseMemory

# 建表语句：单表存 JSON 消息列表
_SCHEMA = """
CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    messages   TEXT NOT NULL,
    updated_at REAL NOT NULL
);
"""


class SQLiteMemory(BaseMemory):
    """基于 SQLite 的持久化存储"""

    def __init__(self, db_path: str) -> None:
        """初始化 SQLite 存储，建表并打开连接

        db_path: 数据库文件路径，父目录不存在时自动创建
        """
        self._db_path = db_path  # 数据库文件路径
        self._lock = threading.Lock()  # 并发访问锁
        parent = Path(db_path).parent
        if str(parent) not in (".", ""):
            parent.mkdir(parents=True, exist_ok=True)
        self._conn = sqlite3.connect(db_path, check_same_thread=False)  # 连接对象
        self._conn.execute(_SCHEMA)
        self._conn.commit()

    def _serialize(self, messages: List[ChatMessage]) -> str:
        """把消息列表序列化成 JSON 字符串

        messages: 待序列化的历史消息列表
        return: JSON 字符串，含 role、content、tool_calls、tool_call_id、name、reasoning_content 字段
        """
        items = []
        for m in messages:
            tool_calls = (
                [
                    {"id": tc.id, "name": tc.name, "arguments": tc.arguments}
                    for tc in m.tool_calls
                ]
                if m.tool_calls
                else None
            )
            items.append(
                {
                    "role": m.role,
                    "content": m.content,
                    "tool_calls": tool_calls,
                    "tool_call_id": m.tool_call_id,
                    "name": m.name,
                    "reasoning_content": m.reasoning_content,
                }
            )
        return json.dumps(items, ensure_ascii=False)

    def _deserialize(self, raw: str) -> List[ChatMessage]:
        """把 JSON 字符串还原成消息列表

        raw: _serialize 产出的 JSON 字符串
        return: 还原后的历史消息列表，tool_calls 字段同步还原为 ToolCall 对象
        """
        items = json.loads(raw)
        msgs = []
        for it in items:
            tool_calls = None
            if it.get("tool_calls"):
                tool_calls = [ToolCall(**tc) for tc in it["tool_calls"]]
            msgs.append(
                ChatMessage(
                    role=it["role"],
                    content=it.get("content", ""),
                    tool_calls=tool_calls,
                    tool_call_id=it.get("tool_call_id"),
                    name=it.get("name"),
                    reasoning_content=it.get("reasoning_content"),
                )
            )
        return msgs

    def load(self, session_id: str) -> Optional[List[ChatMessage]]:
        """加载会话历史，不存在返回 None

        session_id: 要加载的会话标识
        return: 反序列化后的消息列表，无记录时返回 None
        """
        with self._lock:
            row = self._conn.execute(
                "SELECT messages FROM sessions WHERE session_id = ?", (session_id,)
            ).fetchone()
        if row is None:
            return None
        return self._deserialize(row[0])

    def save(self, session_id: str, messages: List[ChatMessage]) -> None:
        """保存会话历史，已存在则覆盖

        session_id: 要保存的会话标识
        messages: 待序列化的历史消息列表，写入 sessions 表并更新时间戳
        """
        raw = self._serialize(messages)
        with self._lock:
            self._conn.execute(
                """INSERT INTO sessions (session_id, messages, updated_at)
                   VALUES (?, ?, ?)
                   ON CONFLICT(session_id) DO UPDATE SET
                       messages = excluded.messages,
                       updated_at = excluded.updated_at""",
                (session_id, raw, time.time()),
            )
            self._conn.commit()

    def delete(self, session_id: str) -> None:
        """删除会话历史

        session_id: 要删除的会话标识，无记录时静默忽略
        """
        with self._lock:
            self._conn.execute("DELETE FROM sessions WHERE session_id = ?", (session_id,))
            self._conn.commit()

    def list_sessions(self, limit: int = 100, offset: int = 0) -> List[str]:
        """列出所有已存 session_id，按更新时间倒序

        limit: 最多返回的会话数，默认 100
        offset: 分页起始偏移，默认 0
        return: 会话标识列表，最近的排在最前
        """
        with self._lock:
            rows = self._conn.execute(
                "SELECT session_id FROM sessions ORDER BY updated_at DESC LIMIT ? OFFSET ?",
                (limit, offset),
            ).fetchall()
        return [r[0] for r in rows]

    def close(self) -> None:
        """关闭数据库连接

        return: 无，关闭后不能再执行读写操作
        """
        with self._lock:
            self._conn.close()
