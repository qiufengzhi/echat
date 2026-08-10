"""
Agent 组装点：把各层创建出来，注入依赖，启动服务

依赖方向（自底向上）：
  providers / tools / memory   → 能力层（具体实现）
  core.orchestrator            → 业务层（依赖上面的抽象接口）
  server                       → 接口层（依赖 core.orchestrator）
  cmd.main                     → 组装点（创建所有对象，注入依赖）

使用方式：
  python -m agent.cmd.main --config agent/config.yaml
"""

from __future__ import annotations

import argparse
import asyncio
import sys
from pathlib import Path

import grpc
from loguru import logger
from pydantic import ValidationError

# 确保项目根目录在 sys.path 中，方便 agent.* 包导入
PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from aiohttp import web

from agent.config.loader import Config, build_parser
from agent.core.orchestrator import ChatOrchestrator
from agent.memory.in_memory import InMemoryMemory
from agent.memory.sqlite_memory import SQLiteMemory
from agent.pb import llm_pb2_grpc
from agent.providers.openai_compatible import OpenAICompatibleProvider
from agent.server.grpc_service import LLMServiceServicer
from agent.server.health import HealthService
from agent.server.http_api import build_http_app
from agent.server.interceptors import ErrorHandlingInterceptor, LoggingInterceptor
from agent.tools.builtin import make_tools
from agent.tools.registry import ToolRegistry


# ---------------------------------------------------------------------------
# 日志配置
# ---------------------------------------------------------------------------
def setup_logging(cfg: Config) -> None:
    """配置 loguru：控制台 + 主日志 + 错误日志三个 handler

    cfg: 全局配置，取其日志级别、目录与保留天数
    """
    # 移除 loguru 默认的 stderr handler
    logger.remove()

    # 控制台输出：带颜色，INFO 及以上
    logger.add(
        sys.stderr,
        format="<green>{time:YYYY-MM-DD HH:mm:ss.SSS}</green> | <level>{level: <8}</level> | <level>{message}</level>",
        level=cfg.logging.level,
        colorize=True,
    )

    log_dir = PROJECT_ROOT / "agent" / cfg.logging.dir
    log_dir.mkdir(parents=True, exist_ok=True)

    # 主日志文件：所有级别，按天滚动
    logger.add(
        str(log_dir / "agent_{time:YYYY-MM-DD}.log"),
        format="{time:YYYY-MM-DD HH:mm:ss.SSS} | {level: <8} | {extra[session]} | {message}",
        level="DEBUG",
        rotation="00:00",
        retention=f"{cfg.logging.retention_days} days",
        encoding="utf-8",
        backtrace=True,
        diagnose=False,
    )

    # 错误日志文件：仅 ERROR 级别
    logger.add(
        str(log_dir / "agent_error_{time:YYYY-MM-DD}.log"),
        format="{time:YYYY-MM-DD HH:mm:ss.SSS} | {level: <8} | {extra[session]} | {message}",
        level="ERROR",
        rotation="00:00",
        retention="30 days",
        encoding="utf-8",
    )

    # 为模块级日志设置 session 默认值，避免非 Session 代码写文件时 KeyError
    logger.configure(extra={"session": ""})


# ---------------------------------------------------------------------------
# 依赖组装
# ---------------------------------------------------------------------------
def build_orchestrator(cfg: Config) -> tuple[ChatOrchestrator, ToolRegistry]:
    """创建 provider / tools / memory，注入 orchestrator，返回 (orchestrator, tool_registry)

    cfg: 全局配置，据此构造各层对象
    return: (编排引擎, 工具注册表)，注册表待 serve 阶段异步注册工具
    """
    provider = OpenAICompatibleProvider(
        api_key=cfg.llm.api_key,
        base_url=cfg.llm.base_url,
        model=cfg.llm.model,
        max_tokens=cfg.llm.max_tokens,
        temperature=cfg.llm.temperature,
        request_timeout=cfg.llm.request_timeout,
    )

    tool_registry = ToolRegistry(default_timeout=cfg.tools.tool_timeout)

    if cfg.memory.backend == "sqlite":
        memory = SQLiteMemory(cfg.memory.sqlite_path)
    else:
        memory = InMemoryMemory()

    orchestrator = ChatOrchestrator(
        provider=provider,
        tool_registry=tool_registry,
        memory=memory,
        config=cfg,
    )
    return orchestrator, tool_registry


async def serve(cfg: Config, orchestrator: ChatOrchestrator, tool_registry: ToolRegistry) -> None:
    """启动 gRPC aio server

    cfg: 全局配置，取其工具开关、监听地址与端口
    orchestrator: 编排引擎，注入到 gRPC servicer
    tool_registry: 工具注册表，serve 阶段按配置注册内置工具
    """
    # 注册内置工具（启动阶段，异步）
    if cfg.tools.enabled:
        for tool in make_tools(cfg.tools.allowed_tools, cfg.search):
            await tool_registry.register(tool)
        logger.info("已启用工具: {}", tool_registry.list_names())

    # 拦截器：日志 + 错误处理（Timeout 默认不启用，双向流是长连接）
    interceptors = [
        LoggingInterceptor(),
        ErrorHandlingInterceptor(),
    ]
    server = grpc.aio.server(interceptors=interceptors)

    health_service = HealthService(orchestrator)
    servicer = LLMServiceServicer(orchestrator, health_service=health_service)
    llm_pb2_grpc.add_LLMServiceServicer_to_server(servicer, server)

    addr = f"{cfg.server.host}:{cfg.server.port}"
    server.add_insecure_port(addr)
    await server.start()
    logger.info(
        "Agent gRPC server listening on {} (model={}, provider={}, strategy={})",
        addr, cfg.llm.model, cfg.llm.provider, cfg.strategy,
    )

    # 启动 HTTP 调试接口（与 gRPC 同进程、同事件循环，端口见配置 http.port）
    http_runner = None
    if cfg.http.enabled:
        app = build_http_app(orchestrator)
        http_runner = web.AppRunner(app)
        await http_runner.setup()
        site = web.TCPSite(http_runner, cfg.http.host, cfg.http.port)
        await site.start()
        logger.info("HTTP 调试接口: http://{}:{}/api/v1/chat", cfg.http.host, cfg.http.port)

    try:
        await server.wait_for_termination()
    except KeyboardInterrupt:
        logger.info("Agent gRPC 服务正在关闭...")
        await server.stop(5)
    finally:
        if http_runner is not None:
            await http_runner.cleanup()


def main() -> None:
    """命令行入口：解析参数 → 加载配置 → 启动服务"""
    parser = build_parser()
    args = parser.parse_args()

    # 将相对路径的 --config 解析为相对于项目根目录的绝对路径
    config_path = args.config
    config_file = Path(config_path)
    if not config_file.is_absolute():
        config_file = PROJECT_ROOT / config_file

    try:
        cfg = Config.load(str(config_file))
    except ValidationError:
        logger.error("配置无效，请检查配置文件或环境变量")
        sys.exit(1)
    cfg.apply_cli(args)

    setup_logging(cfg)

    # 组装依赖（tools 在 serve 里异步注册，避免启动前需要事件循环）
    orchestrator, tool_registry = build_orchestrator(cfg)
    asyncio.run(serve(cfg, orchestrator, tool_registry))


if __name__ == "__main__":
    main()
