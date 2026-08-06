"""业务层：全部业务逻辑在这一层"""

from __future__ import annotations

from typing import Any


def __getattr__(name: str) -> Any:
    """惰性导出，避免 __init__ 在加载时强依赖子模块

    name: 要访问的模块属性名，仅支持 ChatOrchestrator 与 Session
    return: 对应的类对象，属性名不存在时抛出 AttributeError
    """
    if name == "ChatOrchestrator":
        from .orchestrator import ChatOrchestrator
        return ChatOrchestrator
    if name == "Session":
        from .session import Session
        return Session
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
