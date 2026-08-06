"""内置工具集合"""

from __future__ import annotations

from typing import List, Optional

from agent.config.loader import SearchConfig
from agent.tools.base import BaseTool
from agent.tools.builtin.get_time import GetTimeTool
from agent.tools.builtin.room_status import RoomStatusTool
from agent.tools.builtin.web_search import WebSearchTool

# 无需配置参数的内置工具，模块加载时即可实例化
_ALL_PARAMETERLESS_TOOLS: List[BaseTool] = [
    GetTimeTool(),
    RoomStatusTool(),
]


def make_tools(
    allowed: Optional[List[str]] = None,
    search_config: Optional[SearchConfig] = None,
) -> List[BaseTool]:
    """按允许列表构造内置工具，allowed 为空则全部启用

    allowed: 允许的工具名列表，为空或 None 时返回全部内置工具
    search_config: 联网搜索配置，为 None 时 WebSearchTool 从环境变量 DOUBAO_SEARCH_API_KEY 读取
    return: 筛选后的工具实例列表
    """
    sc = search_config
    web_search = WebSearchTool(
        api_key=sc.api_key if sc else "",
        base_url=sc.base_url if sc else "https://open.feedcoopapi.com/search_api/global_search",
        timeout=sc.timeout if sc else 10.0,
    )
    tools: List[BaseTool] = _ALL_PARAMETERLESS_TOOLS + [web_search]
    if allowed:
        return [t for t in tools if t.name in allowed]
    return tools
