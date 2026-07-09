"""
LLM gRPC 服务：通过双向流与后端通信，逐 token 流式回传 LLM 回复。

架构：
  后端 --gRPC 双向流--> LLM 服务 --OpenAI SDK--> DeepSeek API
  后端发送 ASR 识别后的用户文本，LLM 流式回传生成结果。

使用方式：
  python -m agent.cmd.main --config agent/config.yaml

配置加载优先级（高到低）：
  1. 命令行参数
  2. 环境变量
  3. config.yaml 配置文件
"""

import argparse
import os
import sys
import threading
from collections import deque
from concurrent import futures
from pathlib import Path
from typing import Deque, Dict, Generator, Iterator, List, Optional

import grpc
import yaml
from loguru import logger
from openai import OpenAI

# 确保项目根目录在 sys.path 中，方便 pb 模块导入
PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from agent.pb import llm_pb2, llm_pb2_grpc

# ----- 日志配置 -----
# 移除 loguru 默认的 stderr handler
logger.remove()

# 控制台输出：带颜色，INFO 及以上
logger.add(
    sys.stderr,
    format="<green>{time:YYYY-MM-DD HH:mm:ss.SSS}</green> | <level>{level: <8}</level> | <level>{message}</level>",
    level="INFO",
    colorize=True,
)

# 主日志文件：所有级别，按天滚动，保留 7 天
logger.add(
    str(PROJECT_ROOT / "agent" / "logs" / "agent_{time:YYYY-MM-DD}.log"),
    format="{time:YYYY-MM-DD HH:mm:ss.SSS} | {level: <8} | {extra[session]} | {message}",
    level="DEBUG",
    rotation="00:00",
    retention="7 days",
    encoding="utf-8",
    backtrace=True,
    diagnose=False,
)

# 错误日志文件：仅 ERROR 级别，保留 30 天，用于长期过错索引
logger.add(
    str(PROJECT_ROOT / "agent" / "logs" / "agent_error_{time:YYYY-MM-DD}.log"),
    format="{time:YYYY-MM-DD HH:mm:ss.SSS} | {level: <8} | {extra[session]} | {message}",
    level="ERROR",
    rotation="00:00",
    retention="30 days",
    encoding="utf-8",
)

# 为模块级日志设置 session 默认值，避免非 AgentSession 代码写文件时 KeyError
logger.configure(extra={"session": ""})

# ----- 默认系统提示词（语音房聊天助手）-----
DEFAULT_SYSTEM_PROMPT = (
    "你是一个语音聊天室里的 AI 聊天助手，用户通过语音跟你说话。"
    "你的回复应该简短、自然、口语化，像真人朋友聊天一样。"
    "不要用 Markdown 格式，不要输出列表或表格。"
    "每次回复控制在 2-4 句话以内。"
    "如果用户问你技术问题，用通俗语言解释，不要像念文档。"
    "可以适当带一点幽默感和情绪，但不要太过。"
    "用户在语音房里和你聊天，所以不要说「让我查一下」或「根据搜索结果」这类话。"
)


# =============================================================================
# Config：配置加载逻辑
# =============================================================================

class Config:
    """配置类：从 YAML 文件和环境变量加载配置。"""

    def __init__(self, config_path: Optional[str] = None) -> None:
        # 默认值
        self.server_host = "0.0.0.0"
        self.server_port = 50053
        self.llm_api_key = ""
        self.llm_base_url = "https://api.deepseek.com/v1"
        self.llm_model = "deepseek-v4-flash"
        self.llm_system_prompt = DEFAULT_SYSTEM_PROMPT
        self.llm_max_tokens = 1024
        self.llm_temperature = 0.7

        # 读取配置文件
        if config_path:
            self._load_from_file(config_path)

        # 环境变量覆盖（优先级：环境变量 > 配置文件）
        self._load_from_env()

    def _load_from_file(self, config_path: str) -> None:
        """从 YAML 配置文件加载配置。"""
        path = Path(config_path)
        if not path.exists():
            logger.warning("配置文件不存在: {}", config_path)
            return

        try:
            with open(path, "r", encoding="utf-8") as f:
                data = yaml.safe_load(f)

            if "server" in data:
                s = data["server"]
                if "host" in s:
                    self.server_host = s["host"]
                if "port" in s:
                    self.server_port = int(s["port"])

            if "llm" in data:
                llm = data["llm"]
                if "api_key" in llm:
                    self.llm_api_key = llm["api_key"]
                if "base_url" in llm:
                    self.llm_base_url = llm["base_url"]
                if "model" in llm:
                    self.llm_model = llm["model"]
                if "system_prompt" in llm:
                    self.llm_system_prompt = llm["system_prompt"]
                if "max_tokens" in llm:
                    self.llm_max_tokens = int(llm["max_tokens"])
                if "temperature" in llm:
                    self.llm_temperature = float(llm["temperature"])

            logger.info("配置文件加载成功: {}", config_path)
        except Exception:
            logger.exception("配置文件解析失败: {}", config_path)

    def _load_from_env(self) -> None:
        """从环境变量覆盖配置。"""
        if os.getenv("AGENT_HOST"):
            self.server_host = os.getenv("AGENT_HOST")
        if os.getenv("AGENT_PORT"):
            self.server_port = int(os.getenv("AGENT_PORT"))
        if os.getenv("LLM_API_KEY"):
            self.llm_api_key = os.getenv("LLM_API_KEY")
        if os.getenv("LLM_BASE_URL"):
            self.llm_base_url = os.getenv("LLM_BASE_URL")
        if os.getenv("LLM_MODEL"):
            self.llm_model = os.getenv("LLM_MODEL")
        if os.getenv("LLM_SYSTEM_PROMPT"):
            self.llm_system_prompt = os.getenv("LLM_SYSTEM_PROMPT")
        if os.getenv("LLM_MAX_TOKENS"):
            self.llm_max_tokens = int(os.getenv("LLM_MAX_TOKENS"))
        if os.getenv("LLM_TEMPERATURE"):
            self.llm_temperature = float(os.getenv("LLM_TEMPERATURE"))

    def validate(self) -> bool:
        """验证配置是否完整。"""
        if not self.llm_api_key:
            logger.error("LLM_API_KEY 未设置。请在配置文件或环境变量中提供。")
            return False
        return True


# =============================================================================
# AgentSession：管理单会话的 LLM 对话上下文
# =============================================================================

class AgentSession:
    """单个会话的 LLM 代理，维护多轮对话历史。"""

    def __init__(
        self,
        session_id: str,
        model_id: str,
        api_key: str,
        base_url: str,
        system_prompt: str,
        max_tokens: int,
        temperature: float,
        max_history: int = 12,
    ) -> None:
        self.session_id = session_id
        self._model_id = model_id
        self._system_prompt = system_prompt
        self._max_tokens = max_tokens
        self._temperature = temperature
        self._history: Deque[Dict[str, str]] = deque(maxlen=max_history)
        self._client = OpenAI(api_key=api_key, base_url=base_url)
        self._logger = logger.bind(session=session_id)

    def _build_messages(self, user_text: str) -> List[Dict[str, str]]:
        """构建完整的 messages 列表：系统提示词 + 历史 + 当前用户输入。"""
        messages: List[Dict[str, str]] = [
            {"role": "system", "content": self._system_prompt},
        ]
        messages.extend(self._history)
        messages.append({"role": "user", "content": user_text})
        return messages

    def chat_stream(self, user_text: str) -> Generator[str, None, None]:
        """流式调用 LLM，逐 token 产出回复文本。"""
        self._logger.info("user_text={}", user_text)

        full_response: List[str] = []

        try:
            stream = self._client.chat.completions.create(
                model=self._model_id,
                messages=self._build_messages(user_text),
                max_tokens=self._max_tokens,
                temperature=self._temperature,
                stream=True,
            )

            for chunk in stream:
                delta = chunk.choices[0].delta if chunk.choices else None
                if delta and delta.content:
                    token = delta.content
                    full_response.append(token)
                    yield token

        except Exception:
            self._logger.exception("LLM 调用失败")
            yield f"[LLM 调用异常]"
            return

        response_text = "".join(full_response)
        if response_text.strip():
            self._history.append({"role": "user", "content": user_text})
            self._history.append({"role": "assistant", "content": response_text})
            self._logger.info("response={}", response_text)


# =============================================================================
# AgentService：gRPC 双向流服务实现
# =============================================================================

class LLMService(llm_pb2_grpc.LLMServiceServicer):
    """LLM gRPC 服务：管理会话级别的 LLM 实例，处理双向流式聊天。"""

    def __init__(
        self,
        model_id: str,
        api_key: str,
        base_url: str,
        system_prompt: str,
        max_tokens: int,
        temperature: float,
    ) -> None:
        self._model_id = model_id
        self._api_key = api_key
        self._base_url = base_url
        self._system_prompt = system_prompt
        self._max_tokens = max_tokens
        self._temperature = temperature
        self._sessions: Dict[str, AgentSession] = {}
        self._lock = threading.Lock()

    def _get_session(self, session_id: str) -> AgentSession:
        """获取或创建 session 对应的 Agent 实例（线程安全）。"""
        with self._lock:
            if session_id not in self._sessions:
                logger.info("创建新会话 session={}", session_id)
                self._sessions[session_id] = AgentSession(
                    session_id=session_id,
                    model_id=self._model_id,
                    api_key=self._api_key,
                    base_url=self._base_url,
                    system_prompt=self._system_prompt,
                    max_tokens=self._max_tokens,
                    temperature=self._temperature,
                )
            return self._sessions[session_id]

    def ChatStream(
        self,
        request_iterator: Iterator[llm_pb2.LLMRequest],
        context: grpc.ServicerContext,
    ) -> Generator[llm_pb2.LLMResponse, None, None]:
        """双向流式聊天 RPC。"""
        session = None  # type: AgentSession | None

        for request in request_iterator:
            user_text = request.user_text.strip()
            session_id = request.session_id
            room_id = request.room_id
            client_id = request.client_id
            seq = request.seq

            if not user_text or not session_id:
                yield llm_pb2.LLMResponse(
                    session_id=session_id or "",
                    room_id=room_id,
                    client_id=client_id,
                    response_text="",
                    is_final=False,
                    seq=seq,
                )
                continue

            if session is None or session.session_id != session_id:
                session = self._get_session(session_id)

            try:
                for text_chunk in session.chat_stream(user_text):
                    yield llm_pb2.LLMResponse(
                        session_id=session_id,
                        room_id=room_id,
                        client_id=client_id,
                        response_text=text_chunk,
                        is_final=False,
                        seq=seq,
                    )

                yield llm_pb2.LLMResponse(
                    session_id=session_id,
                    room_id=room_id,
                    client_id=client_id,
                    response_text="",
                    is_final=True,
                    seq=seq,
                )

            except Exception:
                logger.exception("聊天流异常 session={}", session_id)
                yield llm_pb2.LLMResponse(
                    session_id=session_id,
                    room_id=room_id,
                    client_id=client_id,
                    response_text="[服务异常]",
                    is_final=True,
                    seq=seq,
                )


# =============================================================================
# 服务启动入口
# =============================================================================

def serve(
    host: str = "0.0.0.0",
    port: int = 50053,
    model_id: Optional[str] = None,
    api_key: Optional[str] = None,
    base_url: Optional[str] = None,
    system_prompt: Optional[str] = None,
    max_tokens: int = 1024,
    temperature: float = 0.7,
) -> None:
    """启动 LLM gRPC 服务。"""

    logger.info(
        "LLM gRPC 服务启动:\n"
        "  host={} port={}\n"
        "  model={}\n"
        "  base_url={}\n"
        "  max_tokens={} temperature={}",
        host, port, model_id, base_url, max_tokens, temperature,
    )

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    llm_pb2_grpc.add_LLMServiceServicer_to_server(
        LLMService(
            model_id=model_id,
            api_key=api_key,
            base_url=base_url,
            system_prompt=system_prompt,
            max_tokens=max_tokens,
            temperature=temperature,
        ),
        server,
    )
    server.add_insecure_port(f"{host}:{port}")
    server.start()
    logger.info("Agent gRPC server listening on {}:{}", host, port)

    import signal
    done = threading.Event()
    signal.signal(signal.SIGINT, lambda *_: done.set())
    signal.signal(signal.SIGTERM, lambda *_: done.set())
    done.wait()
    logger.info("Agent gRPC 服务正在关闭...")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="启动 Agent gRPC 流式服务")
    parser.add_argument("--config", default="agent/config.yaml", help="配置文件路径")
    parser.add_argument("--host", default=None, help="覆盖配置文件中的 server.host")
    parser.add_argument("--port", type=int, default=None, help="覆盖配置文件中的 server.port")
    parser.add_argument("--api-key", default=None, help="覆盖配置文件中的 llm.api_key")
    parser.add_argument("--base-url", default=None, help="覆盖配置文件中的 llm.base_url")
    parser.add_argument("--model-id", default=None, help="覆盖配置文件中的 llm.model")
    parser.add_argument("--system-prompt", default=None, help="覆盖配置文件中的 llm.system_prompt")
    parser.add_argument("--max-tokens", type=int, default=None, help="覆盖配置文件中的 llm.max_tokens")
    parser.add_argument("--temperature", type=float, default=None, help="覆盖配置文件中的 llm.temperature")
    args = parser.parse_args()

    # 加载配置（配置文件 + 环境变量）
    cfg = Config(args.config)

    # 命令行参数覆盖（优先级最高）
    if args.host is not None:
        cfg.server_host = args.host
    if args.port is not None:
        cfg.server_port = args.port
    if args.api_key is not None:
        cfg.llm_api_key = args.api_key
    if args.base_url is not None:
        cfg.llm_base_url = args.base_url
    if args.model_id is not None:
        cfg.llm_model = args.model_id
    if args.system_prompt is not None:
        cfg.llm_system_prompt = args.system_prompt
    if args.max_tokens is not None:
        cfg.llm_max_tokens = args.max_tokens
    if args.temperature is not None:
        cfg.llm_temperature = args.temperature

    # 验证配置
    if not cfg.validate():
        sys.exit(1)

    # 启动服务
    serve(
        host=cfg.server_host,
        port=cfg.server_port,
        model_id=cfg.llm_model,
        api_key=cfg.llm_api_key,
        base_url=cfg.llm_base_url,
        system_prompt=cfg.llm_system_prompt,
        max_tokens=cfg.llm_max_tokens,
        temperature=cfg.llm_temperature,
    )
