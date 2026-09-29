"""
Agent 测试套件（无第三方测试依赖，用 asyncio.run / get_event_loop 驱动协程）

运行方式（在项目根目录）：
  python -m pytest agent/test/ -q

覆盖：
  core/types.py    共享数据类型
  memory/          内存与 SQLite 记忆
  tools/           工具注册表与内置工具
  strategies/      三种自主循环策略（mock provider）
  orchestrator     编排引擎（会话、健康、历史）
  turn servicer    真实 aio server + client（每轮一次 RPC：Generate、CloseSession、HealthCheck）
"""

import asyncio
import sys
import time
from pathlib import Path

# 保证 agent.* 可导入
PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from agent.config.loader import Config
from agent.core.orchestrator import ChatOrchestrator
from agent.core.types import (
    ChatMessage,
    DoneEvent,
    ErrorEvent,
    TokenEvent,
    ToolCall,
    ToolCallEvent,
)
from agent.memory.in_memory import InMemoryMemory
from agent.memory.sqlite_memory import SQLiteMemory
from agent.providers.base import BaseLLMProvider
from agent.tools.builtin import make_tools
from agent.tools.registry import ToolRegistry


# =============================================================================
# 测试用 mock provider / registry
# =============================================================================
class FakeProvider(BaseLLMProvider):
    """可编程的 mock provider，按调用次数返回预设事件流"""

    model_name = "fake-model"  # 当前模型名
    token_limit = 4096  # 上下文窗口大小

    def __init__(self, script=None):
        """初始化假 provider

        script: 预设事件脚本，元素为 (类型, 内容...) 元组，缺省单次 echo
        """
        # script: list of (call_idx → 该轮 yield 的事件)
        self.script = script or [("echo", "你好")]  # 预设事件脚本
        self.calls = 0  # 已调用次数
        self.received: list = []  # 每次调用收到的消息列表，供断言思考内容回传

    async def chat_stream(self, messages, tools=None, cancel_event=None):
        """按调用序号返回预设事件流

        messages: 对话上下文，假实现不解析仅原样接收
        tools: 工具 schema 列表，假实现不使用
        cancel_event: 取消信号，假实现不使用
        return: 按 script 预设事件组成的异步事件流
        """
        self.calls += 1
        self.received.append(list(messages))
        idx = min(self.calls - 1, len(self.script) - 1)
        step = self.script[idx]
        kind = step[0]
        if kind == "echo":
            yield TokenEvent(content=step[1])
            yield DoneEvent(finish_reason="stop")
        elif kind == "tool":
            # 思考模式：tool 轮 DoneEvent 可携带推理内容（step[3]）
            reasoning = step[3] if len(step) > 3 else ""
            yield ToolCallEvent(tool_call_id="tc1", tool_name=step[1], arguments=step[2])
            yield DoneEvent(finish_reason="tool_calls", reasoning_content=reasoning)
        elif kind == "think":
            # 思考模式：正文前先产出推理，再产出最终回复
            yield TokenEvent(content=step[1])
            yield DoneEvent(finish_reason="stop", reasoning_content=step[2])
        elif kind == "error":
            yield ErrorEvent(code=step[1], message=step[2], recoverable=False)
            yield DoneEvent(finish_reason="error")
        elif kind == "cancel":
            yield TokenEvent(content="半句回复")
            yield DoneEvent(cancelled=True, finish_reason="cancelled")


class PacedProvider(BaseLLMProvider):
    """按固定节奏产出的 mock provider，用于验证会话内串行、会话间并行与取消"""

    model_name = "paced-model"  # 当前模型名
    token_limit = 4096  # 上下文窗口大小

    def __init__(self, tokens: int = 6, interval: float = 0.05, text: str = "字"):
        """初始化节奏可控的 mock provider

        tokens: 每轮产出的事件数
        interval: 每个事件之间的间隔秒数
        text: 事件文本前缀，序号自动追加
        """
        self._tokens = tokens  # 每轮事件数
        self._interval = interval  # 事件间隔秒数
        self._text = text  # 事件文本前缀
        self.active = 0  # 当前正在生成的调用数
        self.max_active = 0  # 观测到的最大并发调用数
        self.cancelled = 0  # 被取消的调用次数

    async def chat_stream(self, messages, tools=None, cancel_event=None):
        """按节奏逐事件产出，并记录并发与取消

        messages: 对话上下文，假实现不解析仅原样接收
        tools: 工具 schema 列表，假实现不使用
        cancel_event: 取消信号，假实现不使用
        return: 按节奏产出的事件流
        """
        self.active += 1
        self.max_active = max(self.max_active, self.active)
        try:
            for i in range(self._tokens):
                await asyncio.sleep(self._interval)
                yield TokenEvent(content=f"{self._text}{i}")
            yield DoneEvent(finish_reason="stop")
        except asyncio.CancelledError:
            self.cancelled += 1
            raise
        finally:
            self.active -= 1


async def make_fake_registry():
    """构造带 get_time 的假注册表，不依赖网络

    return: 已注册 get_time 工具的注册表
    """
    reg = ToolRegistry(default_timeout=5.0)
    for tool in make_tools(["get_time"]):
        await reg.register(tool)
    return reg


_BASE_CONFIG = {
    "server": {"host": "127.0.0.1", "port": 50053},
    "http": {"enabled": False, "host": "127.0.0.1", "port": 40001},
    "llm": {
        "provider": "fake",
        "api_key": "test-key",
        "base_url": "http://fake",
        "model": "fake",
        "system_prompt": "测试提示词",
        "max_tokens": 100,
        "temperature": 0.7,
        "request_timeout": 5.0,
    },
    "session": {"ttl_seconds": 3600, "max_history": 12, "max_sessions": 100},
    "strategy": "simple",
    "tools": {"enabled": True, "max_rounds": 5, "tool_timeout": 5.0, "allowed_tools": []},
    "search": {"api_key": "test-search-key", "base_url": "http://fake-search", "timeout": 1.0},
    "memory": {"backend": "in_memory", "sqlite_path": "agent/data/test.db"},
    "logging": {"level": "INFO", "dir": "logs", "retention_days": 1},
}


def _merge_dict(base, override):
    """递归合并两个字典，override 覆盖 base

    base: 被覆盖的基础字典，即默认配置
    override: 覆盖字典，同键的值覆盖 base，不修改入参
    return: 合并后的新字典
    """
    result = dict(base)
    for k, v in override.items():
        if isinstance(v, dict) and isinstance(result.get(k), dict):
            result[k] = _merge_dict(result[k], v)
        else:
            result[k] = v
    return result


def make_config(**overrides):
    """用基础配置 + 覆盖项构造 Config 实例

    overrides: 按配置段名传的覆盖项，如 tools={"enabled": False}
    return: 校验通过后的 Config 实例
    """
    return Config(**_merge_dict(_BASE_CONFIG, overrides))


def run(coro):
    """同步驱动协程，替代 pytest-asyncio

    coro: 待执行的协程对象
    return: 协程运行结果，异常时向上抛出
    """
    loop = asyncio.new_event_loop()
    try:
        return loop.run_until_complete(coro)
    finally:
        loop.close()


# =============================================================================
# 1. core/types.py
# =============================================================================
class TestTypes:
    """core/types.py 数据类型的测试"""

    def test_chat_message(self):
        """ChatMessage 基本字段"""
        m = ChatMessage(role="user", content="hi")
        assert m.role == "user"
        assert m.content == "hi"

    def test_tool_call(self):
        """ToolCall 基本字段"""
        tc = ToolCall(id="1", name="get_time", arguments="{}")
        assert tc.name == "get_time"

    def test_error_code_values(self):
        """ErrorCode 枚举值与期望一致"""
        from agent.core.types import ErrorCode

        assert ErrorCode.LLM_TIMEOUT.value == "LLM_TIMEOUT"
        assert ErrorCode.TOOL_NOT_FOUND.value == "TOOL_NOT_FOUND"


# =============================================================================
# 2. memory
# =============================================================================
class TestMemory:
    """memory 存储实现的测试"""

    def test_in_memory(self):
        """InMemoryMemory 保存/加载/删除"""
        mem = InMemoryMemory()
        mem.save("s1", [ChatMessage(role="user", content="a")])
        assert mem.load("s1")[0].content == "a"
        assert mem.list_sessions() == ["s1"]
        mem.delete("s1")
        assert mem.load("s1") is None

    def test_sqlite(self, tmp_path):
        """SQLiteMemory 保存/加载

        tmp_path: pytest 提供的临时目录，用于隔离测试数据库文件
        """
        db = str(tmp_path / "m.db")
        mem = SQLiteMemory(db)
        mem.save("s1", [ChatMessage(role="assistant", content="b")])
        loaded = mem.load("s1")
        assert loaded[0].role == "assistant"
        assert loaded[0].content == "b"
        mem.close()

    def test_sqlite_reasoning(self, tmp_path):
        """SQLiteMemory 保存/加载 reasoning_content，保证重启后多轮对话可用

        tmp_path: pytest 提供的临时目录，用于隔离测试数据库文件
        """
        db = str(tmp_path / "m.db")
        mem = SQLiteMemory(db)
        mem.save("s1", [ChatMessage(role="assistant", content="b", reasoning_content="思考")])
        loaded = mem.load("s1")
        assert loaded[0].reasoning_content == "思考"
        mem.close()


# =============================================================================
# 3. tools
# =============================================================================
class TestTools:
    """tools 注册表的测试"""

    def test_registry_validate(self):
        """多余参数被 JSON Schema 拒绝"""

        async def inner():
            """验证多余参数被 JSON Schema 拒绝"""
            reg = await make_fake_registry()
            r = await reg.execute("get_time", {"bad_param": 1})
            assert r.is_error  # get_time 无参数，额外参数会被 JSON Schema 拒绝
            r = await reg.execute("get_time", {})
            assert not r.is_error
            assert r.result

        run(inner())

    def test_missing_tool(self):
        """不存在的工具返回错误结果"""

        async def inner():
            """验证不存在的工具返回错误结果"""
            reg = await make_fake_registry()
            r = await reg.execute("nope", {})
            assert r.is_error

        run(inner())


# =============================================================================
# 3.5 provider 消息序列化
# =============================================================================
class TestProviderMessages:
    """provider 层消息格式的测试"""

    def test_to_openai_messages_reasoning(self):
        """assistant 消息回传 reasoning_content，其余角色不带"""
        from agent.providers.openai_compatible import _to_openai_messages

        out = _to_openai_messages(
            [
                ChatMessage(role="assistant", content="回答", reasoning_content="推理"),
                ChatMessage(role="user", content="问"),
            ]
        )
        assert out[0]["reasoning_content"] == "推理"
        assert "reasoning_content" not in out[1]

    def test_to_openai_messages_tool_reasoning(self):
        """带工具调用的 assistant 消息同样回传 reasoning_content"""
        from agent.providers.openai_compatible import _to_openai_messages

        out = _to_openai_messages(
            [
                ChatMessage(
                    role="assistant",
                    content="",
                    reasoning_content="推理",
                    tool_calls=[ToolCall(id="c1", name="get_time", arguments="{}")],
                )
            ]
        )
        assert out[0]["reasoning_content"] == "推理"
        assert out[0]["tool_calls"][0]["function"]["name"] == "get_time"


# =============================================================================
# 4. strategies
# =============================================================================
class TestStrategies:
    """策略层的测试"""

    def test_simple_echo(self):
        """simple 策略一问一答"""

        async def inner():
            """验证 simple 策略一问一答"""
            from agent.core.strategies import SimpleStrategy

            cfg = make_config()
            reg = await make_fake_registry()
            prov = FakeProvider([("echo", "你好")])
            msgs = [ChatMessage(role="user", content="你好")]
            evts = [e async for e in SimpleStrategy().execute(prov, reg, msgs, None, cfg)]
            kinds = [type(e).__name__ for e in evts]
            assert "TokenEvent" in kinds
            assert "DoneEvent" in kinds
            assert prov.calls == 1

        run(inner())

    def test_simple_tool_loop(self):
        """simple 策略工具循环一轮后回答"""

        async def inner():
            """验证 simple 策略走一轮工具循环后回答"""
            from agent.core.strategies import SimpleStrategy

            cfg = make_config()
            reg = await make_fake_registry()
            prov = FakeProvider([("tool", "get_time", "{}"), ("echo", "现在是12点")])
            msgs = [ChatMessage(role="user", content="几点？")]
            evts = [e async for e in SimpleStrategy().execute(prov, reg, msgs, None, cfg)]
            kinds = [type(e).__name__ for e in evts]
            assert "ToolCallEvent" in kinds
            assert "ToolResultEvent" in kinds
            assert prov.calls == 2

        run(inner())

    def test_simple_tool_loop_reasoning(self):
        """工具循环把每轮思考内容挂到 assistant tool_call 消息上，结束事件回传末轮思考"""

        async def inner():
            """验证思考模式下工具循环的消息携带 reasoning_content"""
            from agent.core.strategies import SimpleStrategy

            cfg = make_config()
            reg = await make_fake_registry()
            prov = FakeProvider(
                [("tool", "get_time", "{}", "推理1"), ("think", "答案", "推理2")]
            )
            msgs = [ChatMessage(role="user", content="几点？")]
            evts = [e async for e in SimpleStrategy().execute(prov, reg, msgs, None, cfg)]

            # 第 2 次调用 LLM 时，历史里的 assistant tool_call 消息带上了第一轮思考
            second_msgs = prov.received[1]
            tool_assistant = [
                m for m in second_msgs if m.role == "assistant" and m.tool_calls
            ]
            assert tool_assistant and tool_assistant[0].reasoning_content == "推理1"

            # 结束事件携带最后一轮思考内容
            done = [e for e in evts if isinstance(e, DoneEvent)]
            assert done and done[-1].reasoning_content == "推理2"

        run(inner())

    def test_react_fallback_without_tools(self):
        """react 策略无工具时降级为 simple"""

        async def inner():
            """验证 react 策略无工具时降级为 simple"""
            from agent.core.strategies import ReActStrategy

            cfg = make_config(tools={"enabled": False})
            reg = await make_fake_registry()
            prov = FakeProvider([("echo", "直接回答")])
            msgs = [ChatMessage(role="user", content="hi")]
            evts = [e async for e in ReActStrategy().execute(prov, reg, msgs, None, cfg)]
            assert any(isinstance(e, TokenEvent) for e in evts)

        run(inner())


# =============================================================================
# 5. orchestrator
# =============================================================================
class TestOrchestrator:
    """编排引擎的测试"""

    def test_handle_message_history(self):
        """handle_message 后历史写入 user 与 assistant 消息"""

        async def inner():
            """验证 handle_message 后历史写入 user 与 assistant 消息"""
            cfg = make_config()
            reg = await make_fake_registry()
            prov = FakeProvider([("echo", "回复内容")])
            orch = ChatOrchestrator(prov, reg, InMemoryMemory(), cfg)
            evts = [e async for e in orch.handle_message("s1", "你好")]
            assert any(isinstance(e, DoneEvent) for e in evts)
            sess = orch._sessions["s1"]
            roles = [m.role for m in sess.get_messages()]
            assert "user" in roles
            assert "assistant" in roles
            assert sess.get_messages()[-1].content == "回复内容"

        run(inner())

    def test_handle_message_reasoning_history(self):
        """handle_message 把思考内容写进 assistant 历史，供下一轮回传"""

        async def inner():
            """验证思考内容随 assistant 消息一起写入历史"""
            cfg = make_config()
            reg = await make_fake_registry()
            prov = FakeProvider([("think", "回复内容", "思考过程")])
            orch = ChatOrchestrator(prov, reg, InMemoryMemory(), cfg)
            [e async for e in orch.handle_message("s1", "你好")]
            sess = orch._sessions["s1"]
            last = sess.get_messages()[-1]
            assert last.role == "assistant"
            assert last.reasoning_content == "思考过程"

        run(inner())

    def test_health(self):
        """health 返回 SERVING 和 provider 名"""

        async def inner():
            """验证 health 返回 SERVING 和 provider 名"""
            cfg = make_config()
            orch = ChatOrchestrator(FakeProvider(), await make_fake_registry(), InMemoryMemory(), cfg)
            hs = await orch.health()
            assert hs.status == "SERVING"
            assert hs.provider_name == "fake-model"

        run(inner())

    def test_session_pool_full(self):
        """会话池达到上限且无可淘汰会话时抛 SESSION_POOL_FULL"""

        async def inner():
            """把上限压到 1，连续创建两个会话验证第二个被拒"""
            cfg = make_config(session={"ttl_seconds": 3600, "max_history": 12, "max_sessions": 1})
            orch = ChatOrchestrator(FakeProvider(), await make_fake_registry(), InMemoryMemory(), cfg)
            first = await orch.get_or_create_session("s-1", "room1")
            assert first is not None
            try:
                await orch.get_or_create_session("s-2", "room2")
            except RuntimeError as exc:
                assert "SESSION_POOL_FULL" in str(exc)
            else:
                raise AssertionError("会话池满时应抛 SESSION_POOL_FULL")

        run(inner())

    def test_memory_restore(self):
        """新建会话时从记忆恢复历史"""

        async def inner():
            """验证新建会话时从记忆恢复历史"""
            cfg = make_config()
            mem = InMemoryMemory()
            mem.save("s9", [ChatMessage(role="user", content="旧消息")])
            orch = ChatOrchestrator(FakeProvider(), await make_fake_registry(), mem, cfg)
            [e async for e in orch.handle_message("s9", "新消息")]
            sess = orch._sessions["s9"]
            contents = [m.content for m in sess.get_messages()]
            assert "旧消息" in contents

        run(inner())


# =============================================================================
# 6. 每轮一次 RPC 的服务实现（真实起一个 aio server + client）
# =============================================================================
async def make_turn_stub(provider):
    """起真实 aio server，返回客户端 stub 与相关对象

    provider: 注入编排引擎的 mock provider
    return: (stub, channel, server, orchestrator)，调用方负责关闭通道与服务端
    """
    import grpc

    from agent.pb import llm_pb2_grpc
    from agent.server.health import HealthService
    from agent.server.interceptors import ErrorHandlingInterceptor, LoggingInterceptor
    from agent.server.turn_service import TurnServiceServicer

    cfg = make_config()
    reg = await make_fake_registry()
    orch = ChatOrchestrator(provider, reg, InMemoryMemory(), cfg)

    server = grpc.aio.server(interceptors=[LoggingInterceptor(), ErrorHandlingInterceptor()])
    servicer = TurnServiceServicer(orch, health_service=HealthService(orch))
    llm_pb2_grpc.add_LLMServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()

    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    return llm_pb2_grpc.LLMServiceStub(channel), channel, server, orch


async def close_turn_server(channel, server):
    """关闭测试用的通道与服务端

    channel: 客户端通道
    server: aio 服务端
    """
    await channel.close()
    await server.stop(0)


async def collect_turn(stub, session_id, turn_id, user_text="你好"):
    """跑完一轮并收集全部响应事件

    stub: 客户端 stub
    session_id: 会话 ID
    turn_id: 轮次 ID
    user_text: 用户输入文本
    return: 本轮收到的响应事件列表
    """
    from agent.pb import llm_pb2

    responses = []
    async for resp in stub.Generate(
        llm_pb2.TurnRequest(
            session_id=session_id,
            room_id="room1",
            client_id="c1",
            turn_id=turn_id,
            user_text=user_text,
            seq=1,
        )
    ):
        responses.append(resp)
    return responses


def test_turn_generate_health_and_close():
    """一轮 Generate 应流式返回回复并以一个结束事件收尾；健康检查与会话释放可用"""

    async def inner():
        """验证 Generate 的正常路径、HealthCheck 与会话释放的幂等语义"""
        from agent.pb import llm_pb2

        stub, channel, server, _ = await make_turn_stub(FakeProvider([("echo", "你好世界")]))
        try:
            hresp = await stub.HealthCheck(llm_pb2.HealthCheckRequest(service="llm"))
            assert hresp.status == llm_pb2.HealthCheckResponse.SERVING

            responses = await collect_turn(stub, "t1", "turn-1")
            texts = [r.response_text for r in responses]
            finals = [r for r in responses if r.is_final]
            assert "你好世界" in texts
            assert len(finals) == 1
            assert all(r.turn_id == "turn-1" for r in responses)

            # 会话释放：首次存在返回 true，重复释放返回 false
            first = await stub.CloseSession(
                llm_pb2.CloseSessionRequest(session_id="t1", reason="room_closed")
            )
            assert first.closed is True
            second = await stub.CloseSession(
                llm_pb2.CloseSessionRequest(session_id="t1", reason="room_closed")
            )
            assert second.closed is False
        finally:
            await close_turn_server(channel, server)

    run(inner())


def test_turn_different_sessions_run_parallel():
    """不同会话的两轮应并行执行"""

    async def inner():
        """两个会话同时发起，断言 provider 侧观测到并发"""
        prov = PacedProvider(tokens=6, interval=0.05)
        stub, channel, server, _ = await make_turn_stub(prov)
        try:
            started = time.monotonic()
            results = await asyncio.gather(
                collect_turn(stub, "s-a", "turn-a"),
                collect_turn(stub, "s-b", "turn-b"),
            )
            elapsed = time.monotonic() - started
            assert prov.max_active == 2, f"不同会话应并行，实际最大并发 {prov.max_active}"
            # 单轮约 0.3s，串行会接近 0.6s，这里给宽松上限
            assert elapsed < 0.55, f"并行耗时异常: {elapsed:.2f}s"
            assert [len(r) for r in results] == [7, 7]
        finally:
            await close_turn_server(channel, server)

    run(inner())


def test_turn_same_session_serialized():
    """同一会话的两轮应串行执行"""

    async def inner():
        """同会话同时发起两轮，断言 provider 侧无并发"""
        prov = PacedProvider(tokens=4, interval=0.05)
        stub, channel, server, _ = await make_turn_stub(prov)
        try:
            await asyncio.gather(
                collect_turn(stub, "s-same", "turn-1"),
                collect_turn(stub, "s-same", "turn-2"),
            )
            assert prov.max_active == 1, f"同一会话应串行，实际最大并发 {prov.max_active}"
        finally:
            await close_turn_server(channel, server)

    run(inner())


def test_turn_cancel_stops_generation_and_releases_lock():
    """取消一轮应立刻停止生成，并释放会话锁（后续轮次可正常结束）"""

    async def inner():
        """取消在途轮次，断言 provider 收到取消且会话可继续使用"""
        from agent.pb import llm_pb2

        prov = PacedProvider(tokens=50, interval=0.05)
        stub, channel, server, _ = await make_turn_stub(prov)
        try:
            call = stub.Generate(
                llm_pb2.TurnRequest(
                    session_id="s-cancel", room_id="room1", client_id="c1",
                    turn_id="turn-c", user_text="你好",
                )
            )
            got_first = False
            async for _resp in call:
                got_first = True
                break
            assert got_first, "取消前应至少收到一个事件"

            call.cancel()
            try:
                async for _resp in call:
                    pass
            except asyncio.CancelledError:
                pass

            for _ in range(100):
                if prov.cancelled == 1:
                    break
                await asyncio.sleep(0.02)
            assert prov.cancelled == 1, "服务端应停止本轮生成"
            assert prov.active == 0, "取消后不应有仍在生成的调用"

            # 会话锁已释放：同一会话再跑一轮能正常结束
            responses = await collect_turn(stub, "s-cancel", "turn-d", user_text="再来一句")
            assert any(r.is_final for r in responses)
        finally:
            await close_turn_server(channel, server)

    run(inner())
