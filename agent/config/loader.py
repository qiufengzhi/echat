"""
配置模块：用 pydantic 代替手写逐字段解析

配置值一律来自配置文件（或环境变量 / 命令行覆盖），代码里不写任何默认值，
字段必填，缺了就报错，保证「配置文件是唯一事实来源」

加载优先级（与 Go viper 一致，从低到高）：
  配置文件 < 环境变量 < 命令行参数

用法：
  cfg = Config.load("agent/config.yaml")
  cfg.apply_cli(args)
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

import yaml
from loguru import logger
from pydantic import BaseModel, ConfigDict, ValidationError


# ---------------------------------------------------------------------------
# 各配置段模型（字段必填，无默认值；extra=forbid 拦截拼写错误的键）
# ---------------------------------------------------------------------------
class ServerConfig(BaseModel):
    """服务监听配置"""

    model_config = ConfigDict(extra="forbid")

    host: str  # 监听地址
    port: int  # 监听端口


class HttpConfig(BaseModel):
    """HTTP 调试接口配置"""

    model_config = ConfigDict(extra="forbid")

    enabled: bool  # 是否启用 HTTP 调试接口
    host: str  # HTTP 监听地址
    port: int  # HTTP 监听端口


class LLMConfig(BaseModel):
    """LLM 提供商配置"""

    model_config = ConfigDict(extra="forbid")

    provider: str  # 提供商名，目前是 deepseek
    api_key: str  # API 密钥
    base_url: str  # OpenAI 兼容接口地址
    model: str  # 模型名
    system_prompt: str  # 系统提示词
    max_tokens: int  # 单次回复最大 token 数
    temperature: float  # 采样温度
    request_timeout: float  # 单次请求超时秒数


class SessionConfig(BaseModel):
    """会话生命周期配置"""

    model_config = ConfigDict(extra="forbid")

    ttl_seconds: float  # 会话过期秒数
    max_history: int  # 单会话最大历史消息数
    max_sessions: int  # 会话池上限


class ToolsConfig(BaseModel):
    """工具系统配置"""

    model_config = ConfigDict(extra="forbid")

    enabled: bool  # 是否启用工具
    max_rounds: int  # 单次请求工具循环最大轮次
    tool_timeout: float  # 单个工具执行超时秒数
    allowed_tools: List[str]  # 允许的工具名列表，空 = 全部可用


class MemoryConfig(BaseModel):
    """记忆持久化配置"""

    model_config = ConfigDict(extra="forbid")

    backend: str  # 后端类型，in_memory | sqlite
    sqlite_path: str  # sqlite 数据库文件路径


class SearchConfig(BaseModel):
    """联网搜索配置"""

    model_config = ConfigDict(extra="forbid")

    api_key: str  # 豆包搜索 API Key
    base_url: str  # 搜索接口地址
    timeout: float  # 搜索请求超时秒数


class LoggingConfig(BaseModel):
    """日志配置"""

    model_config = ConfigDict(extra="forbid")

    level: str  # 控制台日志级别
    dir: str  # 日志目录
    retention_days: int  # 日志保留天数


# ---------------------------------------------------------------------------
# 环境变量 → 配置字段 的映射表（viper 风格：环境变量优先于配置文件）
# ---------------------------------------------------------------------------
_ENV_MAP: List[Tuple[str, Tuple[str, ...]]] = [
    ("AGENT_HOST", ("server", "host")),
    ("AGENT_PORT", ("server", "port")),
    ("AGENT_HTTP_ENABLED", ("http", "enabled")),
    ("AGENT_HTTP_HOST", ("http", "host")),
    ("AGENT_HTTP_PORT", ("http", "port")),
    ("LLM_PROVIDER", ("llm", "provider")),
    ("LLM_API_KEY", ("llm", "api_key")),
    ("LLM_BASE_URL", ("llm", "base_url")),
    ("LLM_MODEL", ("llm", "model")),
    ("LLM_SYSTEM_PROMPT", ("llm", "system_prompt")),
    ("LLM_MAX_TOKENS", ("llm", "max_tokens")),
    ("LLM_TEMPERATURE", ("llm", "temperature")),
    ("LLM_REQUEST_TIMEOUT", ("llm", "request_timeout")),
    ("SESSION_TTL_SECONDS", ("session", "ttl_seconds")),
    ("SESSION_MAX_HISTORY", ("session", "max_history")),
    ("SESSION_MAX_SESSIONS", ("session", "max_sessions")),
    ("STRATEGY", ("strategy",)),
    ("TOOLS_ENABLED", ("tools", "enabled")),
    ("TOOLS_MAX_ROUNDS", ("tools", "max_rounds")),
    ("TOOLS_TIMEOUT", ("tools", "tool_timeout")),
    ("MEMORY_BACKEND", ("memory", "backend")),
    ("MEMORY_SQLITE_PATH", ("memory", "sqlite_path")),
    ("SEARCH_API_KEY", ("search", "api_key")),
    ("SEARCH_BASE_URL", ("search", "base_url")),
    ("SEARCH_TIMEOUT", ("search", "timeout")),
    ("LOGGING_LEVEL", ("logging", "level")),
    ("LOGGING_DIR", ("logging", "dir")),
    ("LOGGING_RETENTION_DAYS", ("logging", "retention_days")),
]


class Config(BaseModel):
    """顶层配置：由各段模型组合而成"""

    model_config = ConfigDict(extra="forbid")

    server: ServerConfig  # 服务监听配置
    http: HttpConfig  # HTTP 调试接口配置
    llm: LLMConfig  # LLM 提供商配置
    session: SessionConfig  # 会话生命周期配置
    strategy: str  # 自主循环策略名
    tools: ToolsConfig  # 工具系统配置
    search: SearchConfig  # 联网搜索配置
    memory: MemoryConfig  # 记忆持久化配置
    logging: LoggingConfig  # 日志配置

    # ------------------------------------------------------------------
    # 加载
    # ------------------------------------------------------------------
    @classmethod
    def load(cls, config_path: str) -> "Config":
        """从配置文件 + 环境变量构建配置，环境变量优先

        config_path: 配置文件路径，YAML 格式
        return: 校验通过后的配置实例，字段缺失或类型错误时抛出 ValidationError
        """
        yaml_data = _read_yaml(config_path)
        env_data = _env_to_nested_dict()
        merged = _deep_merge(yaml_data, env_data)
        try:
            return cls(**merged)
        except ValidationError as exc:
            logger.error("配置校验失败: {}", exc)
            raise

    # ------------------------------------------------------------------
    # 命令行覆盖（优先级最高）
    # ------------------------------------------------------------------
    def apply_cli(self, args: argparse.Namespace) -> None:
        """用命令行参数覆盖配置值，已设的字段才覆盖

        args: 命令行解析结果，逐属性匹配内置映射表覆盖对应配置段字段
        """
        mapping = {
            "host": ("server", "host"),
            "port": ("server", "port"),
            "api_key": ("llm", "api_key"),
            "base_url": ("llm", "base_url"),
            "model_id": ("llm", "model"),
            "model": ("llm", "model"),
            "system_prompt": ("llm", "system_prompt"),
            "max_tokens": ("llm", "max_tokens"),
            "temperature": ("llm", "temperature"),
            "strategy": ("strategy",),
            "search_api_key": ("search", "api_key"),
            "search_base_url": ("search", "base_url"),
        }
        for name, val in vars(args).items():
            if val is None or name == "config":
                continue
            path = mapping.get(name)
            if path is None:
                continue
            node = self
            for key in path[:-1]:
                node = getattr(node, key)
            setattr(node, path[-1], val)


# ---------------------------------------------------------------------------
# 内部工具函数
# ---------------------------------------------------------------------------
def _read_yaml(config_path: str) -> Dict[str, Any]:
    """读取 YAML 配置文件，缺失或解析失败时返回空字典

    config_path: 配置文件路径，文件不存在或解析失败时仅告警不报错
    return: 解析出的配置字典，缺失或失败时返回空字典
    """
    path = Path(config_path)
    if not path.exists():
        logger.warning("配置文件不存在: {}", config_path)
        return {}
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = yaml.safe_load(f) or {}
        logger.info("配置文件加载成功: {}", config_path)
        return data
    except Exception:
        logger.exception("配置文件解析失败: {}", config_path)
        return {}


def _env_to_nested_dict() -> Dict[str, Any]:
    """把命中的环境变量转成嵌套字典，值保持字符串由 pydantic 做类型转换

    return: 按配置段嵌套的环境变量字典，未命中的变量不出现
    """
    result: Dict[str, Any] = {}
    for env_var, path in _ENV_MAP:
        if env_var not in os.environ:
            continue
        node = result
        for key in path[:-1]:
            node = node.setdefault(key, {})
        node[path[-1]] = os.environ[env_var]
    return result


def _deep_merge(base: Dict[str, Any], override: Dict[str, Any]) -> Dict[str, Any]:
    """递归合并两个字典，override 覆盖 base，用于环境变量覆盖配置文件

    base: 被覆盖的基础字典，即配置文件解析结果
    override: 覆盖字典，即环境变量解析结果，同键的值覆盖 base
    return: 合并后的新字典，不修改入参
    """
    result = dict(base)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = _deep_merge(result[key], value)
        else:
            result[key] = value
    return result


def build_parser() -> argparse.ArgumentParser:
    """构造命令行参数解析器，供 cmd/main.py 使用

    return: 已注册全部覆盖参数的命令行解析器
    """
    parser = argparse.ArgumentParser(description="启动 Agent gRPC 流式服务")
    parser.add_argument("--config", default="agent/config.yaml", help="配置文件路径")
    parser.add_argument("--host", default=None, help="覆盖 server.host")
    parser.add_argument("--port", type=int, default=None, help="覆盖 server.port")
    parser.add_argument("--api-key", default=None, help="覆盖 llm.api_key")
    parser.add_argument("--base-url", default=None, help="覆盖 llm.base_url")
    parser.add_argument("--model-id", default=None, help="覆盖 llm.model")
    parser.add_argument("--system-prompt", default=None, help="覆盖 llm.system_prompt")
    parser.add_argument("--max-tokens", type=int, default=None, help="覆盖 llm.max_tokens")
    parser.add_argument("--temperature", type=float, default=None, help="覆盖 llm.temperature")
    parser.add_argument("--strategy", default=None, help="自主循环策略: simple|react|plan_and_solve")
    parser.add_argument("--search-api-key", default=None, help="覆盖 search.api_key")
    parser.add_argument("--search-base-url", default=None, help="覆盖 search.base_url")
    return parser
