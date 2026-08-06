"""
LLM Provider 抽象接口

core/orchestrator.py 只依赖这里定义的 BaseLLMProvider，不关心底层是
DeepSeek 还是 Ollama。任何 provider 实现只要满足这个接口即可被注入使用
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import AsyncIterator, List, Optional

from agent.core.types import ChatMessage, StreamEvent


class ProviderError(Exception):
    """LLM 调用失败，code 为结构化错误码，供上层映射为 ErrorEvent"""

    def __init__(
        self,
        code: str,
        message: str,
        recoverable: bool = False,
    ) -> None:
        """初始化 LLM 调用异常

        code: 结构化错误码，对应 ErrorCode 枚举值
        message: 人类可读的错误描述
        recoverable: 是否可重试，供上层决定是否自动恢复
        """
        super().__init__(message)
        self.code = code  # 结构化错误码
        self.message = message  # 错误描述
        self.recoverable = recoverable  # 是否可重试


class LLMAuthError(ProviderError):
    """API key 无效"""

    def __init__(self, message: str = "LLM API key 无效或未授权") -> None:
        """初始化鉴权异常，固定错误码 LLM_AUTH_ERROR 且不可重试

        message: 错误描述，默认说明 API key 无效或未授权
        """
        super().__init__(code="LLM_AUTH_ERROR", message=message, recoverable=False)


class LLMRateLimitError(ProviderError):
    """被限流"""

    def __init__(self, message: str = "LLM API 请求被限流") -> None:
        """初始化限流异常，固定错误码 LLM_RATE_LIMITED 且可重试

        message: 错误描述，默认说明请求被限流
        """
        super().__init__(code="LLM_RATE_LIMITED", message=message, recoverable=True)


class LLMTimeoutError(ProviderError):
    """调用超时"""

    def __init__(self, message: str = "LLM API 调用超时") -> None:
        """初始化超时异常，固定错误码 LLM_TIMEOUT 且可重试

        message: 错误描述，默认说明调用超时
        """
        super().__init__(code="LLM_TIMEOUT", message=message, recoverable=True)


class LLMAPIConnectionError(ProviderError):
    """网络/连接错误"""

    def __init__(self, message: str = "LLM API 连接失败") -> None:
        """初始化连接异常，固定错误码 LLM_API_ERROR 且可重试

        message: 错误描述，默认说明连接失败
        """
        super().__init__(code="LLM_API_ERROR", message=message, recoverable=True)


class BaseLLMProvider(ABC):
    """所有 LLM 后端必须实现的接口"""

    @property
    @abstractmethod
    def model_name(self) -> str:
        """当前模型名，用于健康检查和日志"""

    @property
    @abstractmethod
    def token_limit(self) -> int:
        """上下文窗口大小，用于截断策略"""

    def supports_tools(self) -> bool:
        """该后端是否支持 function calling，默认 True，子类可覆盖"""
        return True

    @abstractmethod
    def chat_stream(
        self,
        messages: List[ChatMessage],
        tools: Optional[List[dict]] = None,
        cancel_event: Optional[object] = None,
    ) -> AsyncIterator[StreamEvent]:
        """流式调用 LLM

        messages: 对话上下文（已拼好 system + history + user）
        tools: OpenAI function calling schema 列表，None 表示本轮不需要工具
        cancel_event: 中断信号，一旦 set 立即终止生成
        产出 TokenEvent / ToolCallEvent / DoneEvent 事件流
        调用失败抛 ProviderError 子类，由上层转成 ErrorEvent
        """
        raise NotImplementedError
