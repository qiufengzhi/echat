"""跨语言冒烟用的 Agent 服务端：真实 TurnServiceServicer + 节奏可控的假 provider

不起 HTTP 调试接口，也不连任何外部模型，只用于让 Go 侧 agentcli 打通
「发起一轮 → 收流式事件 → 取消」这条链路

用法（项目根目录，端口可换）：
  python agent/test/turn_server_smoke.py 127.0.0.1:50099
"""

from __future__ import annotations

import asyncio
import sys
import time
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

import grpc  # noqa: E402
from loguru import logger  # noqa: E402

from agent.config.loader import Config  # noqa: E402
from agent.core.orchestrator import ChatOrchestrator  # noqa: E402
from agent.core.types import DoneEvent, StreamEvent, TokenEvent  # noqa: E402
from agent.memory.in_memory import InMemoryMemory  # noqa: E402
from agent.pb import llm_pb2_grpc  # noqa: E402
from agent.providers.base import BaseLLMProvider  # noqa: E402
from agent.server.health import HealthService  # noqa: E402
from agent.server.interceptors import ErrorHandlingInterceptor, LoggingInterceptor  # noqa: E402
from agent.server.turn_service import TurnServiceServicer  # noqa: E402
from agent.tools.registry import ToolRegistry  # noqa: E402

# 冒烟配置：不依赖任何外部服务，工具关闭
SMOKE_CONFIG = {
    "server": {"host": "127.0.0.1", "port": 50099},
    "http": {"enabled": False, "host": "127.0.0.1", "port": 40001},
    "llm": {
        "provider": "fake",
        "api_key": "smoke-key",
        "base_url": "http://fake",
        "model": "fake",
        "system_prompt": "你是冒烟测试助手",
        "max_tokens": 100,
        "temperature": 0.7,
        "request_timeout": 5.0,
    },
    "session": {"ttl_seconds": 3600, "max_history": 12, "max_sessions": 100},
    "strategy": "simple",
    "tools": {"enabled": False, "max_rounds": 1, "tool_timeout": 5.0, "allowed_tools": []},
    "search": {"api_key": "smoke-key", "base_url": "http://fake", "timeout": 1.0},
    "memory": {"backend": "in_memory", "sqlite_path": "agent/data/smoke.db"},
    "logging": {"level": "INFO", "dir": "logs", "retention_days": 1},
}


class PacedProvider(BaseLLMProvider):
    """按固定节奏逐事件产出，便于在流中途观察取消"""

    model_name = "smoke-paced"  # 模型名
    token_limit = 4096  # 上下文窗口

    def __init__(self, tokens: int = 100, interval: float = 0.1) -> None:
        """初始化节奏可控的假 provider

        tokens: 每轮事件数
        interval: 事件间隔秒数
        """
        self._tokens = tokens  # 每轮事件数
        self._interval = interval  # 事件间隔秒数
        self.cancelled = 0  # 被取消次数

    async def chat_stream(self, messages, tools=None, cancel_event=None):
        """逐事件产出，取消时记录并向上抛出

        messages: 对话上下文，假实现不解析
        tools: 工具 schema，假实现不使用
        cancel_event: 取消信号，假实现不使用
        return: 事件流
        """
        try:
            for i in range(self._tokens):
                await asyncio.sleep(self._interval)
                yield TokenEvent(content=f"包{i}。")
            yield DoneEvent(finish_reason="stop")
        except asyncio.CancelledError:
            self.cancelled += 1
            logger.warning("本轮生成被取消")
            raise


async def main(addr: str) -> None:
    """启动冒烟服务端并阻塞运行

    addr: 监听地址，形如 127.0.0.1:50099
    """
    cfg = Config(**SMOKE_CONFIG)
    provider = PacedProvider()
    orchestrator = ChatOrchestrator(provider, ToolRegistry(default_timeout=5.0), InMemoryMemory(), cfg)

    server = grpc.aio.server(interceptors=[LoggingInterceptor(), ErrorHandlingInterceptor()])
    servicer = TurnServiceServicer(orchestrator, health_service=HealthService(orchestrator))
    llm_pb2_grpc.add_LLMServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port(addr)
    await server.start()
    logger.info("冒烟服务端已启动 {}（端口 {}）", addr, port)
    started = time.monotonic()
    try:
        await server.wait_for_termination()
    except KeyboardInterrupt:
        logger.info("收到中断，停止服务端")
    finally:
        await server.stop(0)
        logger.info("运行 {:.1f}s，被取消轮次数 = {}", time.monotonic() - started, provider.cancelled)


if __name__ == "__main__":
    listen = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1:50099"
    asyncio.run(main(listen))
