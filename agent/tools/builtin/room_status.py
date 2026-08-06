"""内置工具：查询房间状态（占位实现，从会话元数据读取）"""

from __future__ import annotations

from typing import Any, Dict

from agent.tools.base import BaseTool


class RoomStatusTool(BaseTool):
    """查询当前语音房间的基本状态信息"""

    name = "room_status"  # 工具名
    description = "查询当前语音房间的基本状态信息。"  # 自然语言描述
    parameters: Dict[str, Any] = {  # JSON Schema 参数定义
        "type": "object",
        "properties": {},
    }

    def __init__(self) -> None:
        """初始化房间状态工具，房间信息提供函数默认未注入"""
        super().__init__()
        # 可选的房间信息注入（由外部维护，Phase 2 预留）
        self._room_info_provider = None  # 房间信息提供函数

    def set_room_info_provider(self, provider) -> None:
        """注入房间信息提供函数

        provider: 无参可调用对象，调用后返回房间状态文本
        """
        self._room_info_provider = provider

    def execute(self, **kwargs) -> str:
        """返回房间状态文本

        kwargs: 无参数，本工具不接收任何入参
        return: 已注入提供函数时返回其文本，否则返回未接入提示
        """
        if self._room_info_provider is not None:
            return self._room_info_provider()
        return "当前房间状态未知（未接入房间服务）"
