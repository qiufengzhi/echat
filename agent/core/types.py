"""
核心共享数据类型（零第三方依赖，纯 dataclass + enum）

这是整个 Agent 的"通用语言"：server/、core/、providers/、tools/、memory/
都用这些类型互相通信，不直接传递裸 dict 或 proto
"""

from __future__ import annotations

import enum
from dataclasses import dataclass
from typing import List, Optional, Union


# ---------------------------------------------------------------------------
# 消息
# ---------------------------------------------------------------------------
class Role(str, enum.Enum):
    """消息角色"""

    SYSTEM = "system"  # 系统消息
    USER = "user"  # 用户消息
    ASSISTANT = "assistant"  # 助手消息
    TOOL = "tool"  # 工具结果消息


@dataclass
class ToolCall:
    """LLM 发起的一次工具调用"""

    id: str  # 工具调用 ID
    name: str  # 工具名
    arguments: str  # 参数 JSON 字符串


@dataclass
class ChatMessage:
    """结构化消息，替代裸 dict"""

    role: str  # 消息角色
    content: str  # 文本内容
    tool_calls: Optional[List[ToolCall]] = None  # assistant 发起的工具调用列表
    tool_call_id: Optional[str] = None  # tool 消息关联的工具调用 ID
    name: Optional[str] = None  # 工具名
    reasoning_content: Optional[str] = None  # 思考模式模型的推理过程，多轮回传历史时必带


@dataclass
class ToolResult:
    """工具执行结果"""

    tool_call_id: str  # 关联的工具调用 ID
    name: str  # 工具名
    result: str  # 执行结果文本
    is_error: bool = False  # 是否执行失败


# ---------------------------------------------------------------------------
# 会话状态
# ---------------------------------------------------------------------------
class SessionState(str, enum.Enum):
    """会话状态机"""

    ACTIVE = "ACTIVE"  # 正常运行
    CANCELLING = "CANCELLING"  # 收到取消信号
    EXPIRED = "EXPIRED"  # 超过 TTL
    REMOVED = "REMOVED"  # 已从池中移除


@dataclass
class SessionMeta:
    """会话元数据（创建/活动时间等）"""

    created_at: float  # 创建时间戳
    last_active_at: float  # 最近活动时间戳


# ---------------------------------------------------------------------------
# 事件流（orchestrator 向上层产出）
# ---------------------------------------------------------------------------
@dataclass
class TokenEvent:
    """一个文本片段"""

    content: str  # 文本内容


@dataclass
class ToolCallEvent:
    """LLM 要求调用工具"""

    tool_call_id: str  # 工具调用 ID
    tool_name: str  # 工具名
    arguments: str  # 参数 JSON 字符串


@dataclass
class ToolResultEvent:
    """工具执行结果"""

    tool_call_id: str  # 关联的工具调用 ID
    name: str  # 工具名
    result: str  # 执行结果文本
    is_error: bool = False  # 是否执行失败


@dataclass
class ErrorEvent:
    """异常"""

    code: str  # 结构化错误码
    message: str  # 错误描述
    recoverable: bool = False  # 是否可恢复重试


@dataclass
class DoneEvent:
    """本轮结束"""

    cancelled: bool = False  # 是否被取消
    finish_reason: str = "stop"  # stop | tool_loop_limit | error
    usage: Optional["TokenUsage"] = None  # token 用量
    reasoning_content: str = ""  # 本轮 LLM 的思考内容，多轮对话回传历史时需携带


# 事件流联合类型：orchestrator 产出到上层的所有可能事件
StreamEvent = Union[TokenEvent, ToolCallEvent, ToolResultEvent, ErrorEvent, DoneEvent]


# ---------------------------------------------------------------------------
# 错误码
# ---------------------------------------------------------------------------
class ErrorCode(str, enum.Enum):
    """结构化错误码"""

    LLM_AUTH_ERROR = "LLM_AUTH_ERROR"  # LLM 鉴权失败
    LLM_RATE_LIMITED = "LLM_RATE_LIMITED"  # LLM 触发限流
    LLM_TIMEOUT = "LLM_TIMEOUT"  # LLM 请求超时
    LLM_API_ERROR = "LLM_API_ERROR"  # LLM 接口错误
    TOOL_NOT_FOUND = "TOOL_NOT_FOUND"  # 工具不存在
    TOOL_TIMEOUT = "TOOL_TIMEOUT"  # 工具执行超时
    TOOL_ARGUMENT_ERROR = "TOOL_ARGUMENT_ERROR"  # 工具参数不合法
    SESSION_NOT_FOUND = "SESSION_NOT_FOUND"  # 会话不存在
    SESSION_POOL_FULL = "SESSION_POOL_FULL"  # 会话池已满
    INTERNAL_ERROR = "INTERNAL_ERROR"  # 内部错误


# ---------------------------------------------------------------------------
# 用量与健康
# ---------------------------------------------------------------------------
@dataclass
class TokenUsage:
    """token 用量统计"""

    prompt_tokens: int = 0  # 输入 token 数
    completion_tokens: int = 0  # 输出 token 数
    total_tokens: int = 0  # 总 token 数


@dataclass
class HealthStatus:
    """健康报告"""

    status: str  # SERVING | DEGRADED | NOT_SERVING
    active_sessions: int = 0  # 活跃会话数
    provider_name: str = ""  # LLM 提供商名
    uptime_seconds: float = 0.0  # 运行时长秒数
    last_error: Optional[str] = None  # 最近一次错误码
