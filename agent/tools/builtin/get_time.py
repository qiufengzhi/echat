"""内置工具：获取当前时间"""

from __future__ import annotations

from datetime import datetime
from typing import Any, Dict

from agent.tools.base import BaseTool


class GetTimeTool(BaseTool):
    """返回当前日期和时间"""

    name = "get_time"  # 工具名
    description = "获取当前日期和时间。用户问现在几点、今天几号时使用。"  # 自然语言描述
    parameters: Dict[str, Any] = {  # JSON Schema 参数定义
        "type": "object",
        "properties": {},
    }

    def execute(self, **kwargs) -> str:
        """返回当前时间字符串

        kwargs: 无参数，本工具不接收任何入参
        return: 形如 2026-08-10 14:30:00 的本地时间字符串
        """
        now = datetime.now()
        return now.strftime("%Y-%m-%d %H:%M:%S")
