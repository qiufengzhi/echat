"""T0 spike（细查）：客户端取消后，服务端到底什么时候、以什么方式感知

第一个 spike 发现：客户端 cancel() 后本地 1.1ms 返回，但服务端 600ms 内
既没触发 add_done_callback，context.cancelled() 也没变真。本脚本用逐拍打印
把两侧时间线摊开，确认服务端究竟靠什么感知取消。

用法（项目根目录）：
  python agent/test/spike_cancel2.py
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

from agent.pb import llm_pb2, llm_pb2_grpc  # noqa: E402

TICK = 0.05  # 每拍间隔秒数


def _log(t0: float, side: str, msg: str) -> None:
    """打印带相对时间戳的日志

    t0: 该侧起点时刻（monotonic）
    side: 侧标识，server 或 client
    msg: 日志正文
    """
    print(f"  [{side} +{(time.monotonic() - t0) * 1000:7.1f}ms] {msg}", flush=True)


class DetailServicer(llm_pb2_grpc.LLMServiceServicer):
    """逐拍打印服务端状态：取消标志、done 标志、回调、异常"""

    async def Generate(self, request, context):
        """逐拍产出事件并打印上下文状态

        request: TurnRequest
        context: gRPC 调用上下文
        return: TurnResponse 事件流
        """
        t0 = time.monotonic()

        def on_done() -> None:
            """RPC 终止回调"""
            _log(t0, "server", f"add_done_callback 触发 cancelled={context.cancelled()}")

        context.add_done_callback(on_done)
        _log(t0, "server", f"handler 开始 cancelled={context.cancelled()} done={context.done()}")

        try:
            for i in range(60):
                await asyncio.sleep(TICK)
                _log(t0, "server", f"i={i:02d} cancelled={context.cancelled()} done={context.done()}")
                yield llm_pb2.TurnResponse(
                    session_id=request.session_id,
                    turn_id=request.turn_id,
                    response_text=f"t{i}",
                    is_final=False,
                )
        except BaseException as exc:  # noqa: BLE001 诊断用：捕捉一切，包括 GeneratorExit
            _log(t0, "server", f"handler 抛出 {type(exc).__name__}: {exc!r}")
            raise
        _log(t0, "server", "handler 正常跑完 60 拍")


async def main() -> None:
    """起服务端与客户端，客户端在 500ms 时取消，观察两侧时间线"""
    server = grpc.aio.server()
    llm_pb2_grpc.add_LLMServiceServicer_to_server(DetailServicer(), server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    stub = llm_pb2_grpc.LLMServiceStub(channel)

    c0 = time.monotonic()
    call = stub.Generate(
        llm_pb2.TurnRequest(session_id="s", room_id="r", client_id="c", turn_id="t-detail", user_text="嗨")
    )

    async def consume() -> int:
        """消费响应直到流结束，返回收到的事件数"""
        n = 0
        async for _resp in call:
            n += 1
            if n == 1:
                _log(c0, "client", "收到首个事件")
        _log(c0, "client", f"响应流结束，共 {n} 个事件")
        return n

    consume_task = asyncio.create_task(consume())
    await asyncio.sleep(0.5)
    _log(c0, "client", "调用 cancel()")
    call.cancel()
    await asyncio.sleep(1.5)

    if consume_task.done():
        exc = None if consume_task.cancelled() else consume_task.exception()
        _log(c0, "client", f"消费 task 已结束，异常={type(exc).__name__ if exc else '无'}")
    else:
        _log(c0, "client", "消费 task 仍未结束")
        consume_task.cancel()

    await channel.close()
    await server.stop(0)


if __name__ == "__main__":
    asyncio.run(main())
