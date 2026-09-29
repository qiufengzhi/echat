"""provider 并发闸门与首包超时的单元测试

不起真实网络：把 provider 内部的 OpenAI 客户端换成可编排的假实现，
只验证「并发上限生效」与「首包超时转成可重试错误」两条契约

运行方式（在项目根目录）：
  python -m pytest agent/test/test_provider_limits.py -q
"""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path
from types import SimpleNamespace

PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from agent.core.types import ChatMessage, DoneEvent, TokenEvent  # noqa: E402
from agent.providers.base import LLMTimeoutError  # noqa: E402
from agent.providers.openai_compatible import OpenAICompatibleProvider  # noqa: E402


def _chunk(text: str, finish_reason=None) -> SimpleNamespace:
    """构造一个 OpenAI 流式分片

    text: 正文增量，空串表示本片无正文
    finish_reason: 结束原因，None 表示未结束
    return: 形如 chat.completion.chunk 的简易对象
    """
    delta = SimpleNamespace(content=text or None, reasoning_content=None, tool_calls=None)
    choice = SimpleNamespace(delta=delta, finish_reason=finish_reason)
    return SimpleNamespace(choices=[choice], usage=None)


class FakeTracker:
    """记录同时在跑的流数量，用于断言并发上限"""

    def __init__(self) -> None:
        """初始化计数"""
        self.active = 0  # 当前并发流数
        self.max_active = 0  # 观测到的最大并发流数
        self.closed = 0  # 已被关闭的流数

    def enter(self) -> None:
        """一条流开始"""
        self.active += 1
        self.max_active = max(self.max_active, self.active)

    def exit(self) -> None:
        """一条流结束"""
        self.active -= 1
        self.closed += 1


class FakeStream:
    """可编排的假上游流：支持首包延迟与逐片间隔"""

    def __init__(
        self,
        tracker: FakeTracker,
        chunks: list,
        first_delay: float = 0.0,
        interval: float = 0.0,
    ) -> None:
        """初始化假流

        tracker: 并发计数器
        chunks: 待产出的分片列表
        first_delay: 首个分片前的延迟秒数，用于制造首包超时
        interval: 分片之间的间隔秒数
        """
        self._tracker = tracker  # 并发计数器
        self._chunks = list(chunks)  # 待产出分片
        self._first_delay = first_delay  # 首包延迟
        self._interval = interval  # 分片间隔
        self._started = False  # 是否已过首包

    def __aiter__(self):
        """返回自身作为异步迭代器

        return: 本对象
        """
        return self

    async def __anext__(self):
        """产出下一个分片

        return: 下一个分片对象
        分片耗尽时抛 StopAsyncIteration
        """
        if not self._started:
            self._started = True
            if self._first_delay:
                await asyncio.sleep(self._first_delay)
        elif self._interval:
            await asyncio.sleep(self._interval)
        if not self._chunks:
            raise StopAsyncIteration
        return self._chunks.pop(0)

    async def close(self) -> None:
        """关闭流并释放计数"""
        self._tracker.exit()


class FakeCompletions:
    """假 chat.completions：create 返回可编排的假流"""

    def __init__(self, tracker: FakeTracker, texts: list, first_delay: float, interval: float) -> None:
        """初始化假 completions

        tracker: 并发计数器
        texts: 每片正文文本
        first_delay: 首包延迟秒数
        interval: 分片间隔秒数
        """
        self._tracker = tracker  # 并发计数器
        self._texts = texts  # 每片正文
        self._first_delay = first_delay  # 首包延迟
        self._interval = interval  # 分片间隔

    async def create(self, **kwargs):
        """模拟建流：返回假流并记一次并发

        kwargs: 原样接收，假实现不解析
        return: 假流对象
        """
        self._tracker.enter()
        chunks = [_chunk(t) for t in self._texts]
        chunks.append(_chunk("", finish_reason="stop"))
        return FakeStream(self._tracker, chunks, self._first_delay, self._interval)


class FakeClient:
    """假 AsyncOpenAI 客户端，只暴露 chat.completions.create"""

    def __init__(self, tracker: FakeTracker, texts: list, first_delay: float = 0.0, interval: float = 0.0) -> None:
        """初始化假客户端

        tracker: 并发计数器
        texts: 每片正文文本
        first_delay: 首包延迟秒数
        interval: 分片间隔秒数
        """
        self.chat = SimpleNamespace(
            completions=FakeCompletions(tracker, texts, first_delay, interval)
        )


def make_provider(
    tracker: FakeTracker,
    texts: list,
    max_concurrency: int = 8,
    first_token_timeout: float = 5.0,
    first_delay: float = 0.0,
    interval: float = 0.0,
) -> OpenAICompatibleProvider:
    """构造注入了假客户端的 provider

    tracker: 并发计数器
    texts: 每片正文文本
    max_concurrency: 并发上限
    first_token_timeout: 首包超时秒数
    first_delay: 首包延迟秒数
    interval: 分片间隔秒数
    return: 可用的 provider 实例
    """
    provider = OpenAICompatibleProvider(
        api_key="test-key",
        base_url="http://fake",
        model="fake-model",
        max_concurrency=max_concurrency,
        first_token_timeout=first_token_timeout,
    )
    provider._client = FakeClient(tracker, texts, first_delay, interval)  # 注入假客户端
    return provider


def _messages() -> list:
    """构造一条最小对话上下文

    return: 仅含一条 user 消息的列表
    """
    return [ChatMessage(role="user", content="你好")]


def run(coro):
    """同步驱动协程，替代 pytest-asyncio

    coro: 待执行的协程对象
    return: 协程运行结果
    """
    loop = asyncio.new_event_loop()
    try:
        return loop.run_until_complete(coro)
    finally:
        loop.close()


def test_concurrency_limit_is_enforced():
    """并发闸门应把同时进行的上游流数压在上限内，且超出的轮次排队后仍能完成"""

    async def inner():
        """起 4 条流、上限 2，断言最大并发不超过 2 且全部完成"""
        tracker = FakeTracker()
        provider = make_provider(tracker, ["甲", "乙"], max_concurrency=2, interval=0.02)

        async def consume() -> int:
            """消费一条流并返回事件数"""
            events = []
            async for event in provider.chat_stream(_messages()):
                events.append(event)
            return len(events)

        counts = await asyncio.gather(*(consume() for _ in range(4)))
        assert all(c > 0 for c in counts), f"每条流都应产出事件，实际 {counts}"
        assert tracker.max_active <= 2, f"并发上限失效，最大并发 {tracker.max_active}"
        assert tracker.closed == 4, f"每条流都应被关闭，实际关闭 {tracker.closed}"

    run(inner())


def test_first_token_timeout_maps_to_retryable_error():
    """首包超过阈值应抛 LLM_TIMEOUT，且标记为可重试"""

    async def inner():
        """首包延迟大于阈值，断言抛出 LLMTimeoutError"""
        tracker = FakeTracker()
        provider = make_provider(tracker, ["甲"], first_token_timeout=0.1, first_delay=0.5)
        try:
            async for _event in provider.chat_stream(_messages()):
                pass
        except LLMTimeoutError as exc:
            assert exc.recoverable is True
            assert exc.code == "LLM_TIMEOUT"
        else:
            raise AssertionError("首包超时应抛 LLMTimeoutError")
        assert tracker.closed == 1, "超时后也应关闭上游流"

    run(inner())


def test_stream_closes_on_cancel():
    """取消（cancel_event 置位）应终止本轮并关闭上游流"""

    async def inner():
        """置位取消事件，断言中间事件为 cancelled 且流已关闭"""
        tracker = FakeTracker()
        provider = make_provider(tracker, ["甲", "乙", "丙"], interval=0.01)
        cancel_event = asyncio.Event()
        tokens = []
        async for event in provider.chat_stream(_messages(), cancel_event=cancel_event):
            if isinstance(event, TokenEvent):
                tokens.append(event.content)
                cancel_event.set()  # 收到首个 token 后立刻取消
            elif isinstance(event, DoneEvent):
                assert event.cancelled is True, "取消后应以 cancelled 结束"
        assert tokens == ["甲"], f"取消后不应继续产出，实际 {tokens}"
        assert tracker.closed == 1, "取消后也应关闭上游流"

    run(inner())
