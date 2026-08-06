"""能力层 - 工具：让 LLM 能调用外部功能"""

from .base import BaseTool
from .registry import ToolRegistry

__all__ = ["BaseTool", "ToolRegistry"]
