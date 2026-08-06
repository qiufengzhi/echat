"""
ToolRegistry：工具注册表 + 执行器

安全约束：
  - 执行前用 JSON Schema 校验参数，不合法直接返回错误，不传给工具
  - 单个工具执行超时（默认 10s），超时返回错误结果
"""

from __future__ import annotations

import asyncio
import json
from typing import Any, Dict, List, Optional

import jsonschema
from loguru import logger

from agent.core.types import ToolResult
from agent.tools.base import BaseTool


class ToolRegistry:
    """注册、查找、执行工具的容器"""

    def __init__(self, default_timeout: float = 10.0) -> None:
        """初始化工具注册表

        default_timeout: 单个工具执行的默认超时秒数，默认 10
        """
        self._tools: Dict[str, BaseTool] = {}  # 工具名 → 工具实例
        self._lock = asyncio.Lock()  # 注册/反注册并发锁
        self._default_timeout = default_timeout  # 单工具执行默认超时秒数

    async def register(self, tool: BaseTool) -> None:
        """注册一个工具

        tool: 工具实例，name 不能为空，重名时覆盖旧实例
        """
        async with self._lock:
            if not tool.name:
                raise ValueError("工具 name 不能为空")
            self._tools[tool.name] = tool
            logger.info("注册工具: {}", tool.name)

    async def unregister(self, name: str) -> None:
        """按名移除工具

        name: 要移除的工具名，不存在时静默忽略
        """
        async with self._lock:
            self._tools.pop(name, None)

    def get(self, name: str) -> Optional[BaseTool]:
        """按名查找工具

        name: 要查找的工具名
        return: 工具实例，未注册时返回 None
        """
        return self._tools.get(name)

    def list_names(self) -> List[str]:
        """列出所有已注册工具名

        return: 已注册工具名列表
        """
        return list(self._tools.keys())

    def get_schemas(self) -> List[Dict[str, Any]]:
        """获取所有工具的 OpenAI schema（给 LLM 用）

        return: 全部已注册工具的 OpenAI function calling schema 列表
        """
        return [t.to_openai_schema() for t in self._tools.values()]

    async def execute(
        self,
        name: str,
        arguments: Dict[str, Any],
        tool_call_id: str = "",
        timeout: Optional[float] = None,
    ) -> ToolResult:
        """执行指定工具，返回结构化的 ToolResult，永不抛异常

        name: 要执行的工具名，未注册时返回错误结果
        arguments: 工具参数字典，先经 JSON Schema 校验再传给工具
        tool_call_id: 关联的工具调用 ID，用于回填到 ToolResult
        timeout: 执行超时秒数，缺省用注册表默认超时
        return: 结构化的执行结果，含成功或失败标记，任何异常都被捕获转为错误结果
        """
        tool = self._tools.get(name)
        if tool is None:
            return ToolResult(
                tool_call_id=tool_call_id, name=name,
                result=f"工具 {name} 暂不可用，请告知用户该功能尚未开放",
                is_error=True,
            )

        # 参数校验：统一收紧为拒绝多余参数，防止 LLM 幻觉出 schema 之外的参数被静默忽略
        # 未声明 additionalProperties 的 schema 默认拒绝多余字段
        try:
            if tool.parameters:
                schema = dict(tool.parameters)
                schema.setdefault("additionalProperties", False)
                jsonschema.validate(arguments, schema)
        except jsonschema.ValidationError as e:
            return ToolResult(
                tool_call_id=tool_call_id, name=name,
                result=f"参数校验失败（{e.message}），请修正参数后重试或告知用户输入有误",
                is_error=True,
            )

        timeout = timeout or self._default_timeout
        loop = asyncio.get_running_loop()
        try:
            # 工具可能是 CPU/IO 密集，丢到线程池执行，不阻塞事件循环
            result = await asyncio.wait_for(
                loop.run_in_executor(None, lambda: tool.execute(**arguments)),
                timeout=timeout,
            )
        except asyncio.TimeoutError:
            logger.warning("工具 {} 执行超时({}s)", name, timeout)
            return ToolResult(
                tool_call_id=tool_call_id, name=name,
                result=f"工具 {name} 请求超时，请告知用户当前无法完成此操作，可稍后重试或改用其他方式回答",
                is_error=True,
            )
        except Exception as e:
            logger.exception("工具 {} 执行失败", name)
            return ToolResult(
                tool_call_id=tool_call_id, name=name,
                result=f"工具 {name} 执行异常，请告知用户服务暂时不可用",
                is_error=True,
            )

        return ToolResult(tool_call_id=tool_call_id, name=name, result=str(result), is_error=False)

    async def execute_json(self, name: str, args_json: str, tool_call_id: str = "") -> ToolResult:
        """从 JSON 字符串参数执行工具

        name: 要执行的工具名
        args_json: 参数 JSON 字符串，空串视为空参数字典
        tool_call_id: 关联的工具调用 ID，用于回填到 ToolResult
        return: 结构化的执行结果，JSON 解析失败时返回错误结果
        """
        try:
            arguments = json.loads(args_json) if args_json else {}
        except json.JSONDecodeError:
            return ToolResult(
                tool_call_id=tool_call_id, name=name,
                result=f"参数 JSON 解析失败: {args_json}", is_error=True,
            )
        return await self.execute(name, arguments, tool_call_id)
