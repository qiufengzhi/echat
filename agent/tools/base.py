"""BaseTool 抽象接口：封装一切可被 LLM 调用的外部功能"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Any, Dict


class BaseTool(ABC):
    """工具抽象基类，子类实现 execute() 即可被注册使用"""

    #: 工具名（LLM 用这个名字指定调哪个工具）
    name: str = ""
    #: 自然语言描述（LLM 据此判断什么时候调用）
    description: str = ""
    #: JSON Schema 参数定义
    parameters: Dict[str, Any] = {}

    @abstractmethod
    def execute(self, **kwargs) -> str:
        """执行工具，返回文本结果（会被塞回 LLM 作为 tool 消息）

        kwargs: 工具参数，键名与 parameters 声明的 JSON Schema 字段对应
        return: 工具执行结果文本，成功或失败均以字符串形式返回
        """

    def to_openai_schema(self) -> Dict[str, Any]:
        """转成 OpenAI function calling 格式

        return: OpenAI 工具的 schema 字典，含 type、function.name、description、parameters
        """
        return {
            "type": "function",
            "function": {
                "name": self.name,
                "description": self.description,
                "parameters": self.parameters,
            },
        }
