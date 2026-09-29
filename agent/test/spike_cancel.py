"""T0 spike：验证每轮一次 RPC 的并发与取消语义（grpc.aio 服务端）

要回答三个问题（结论决定 servicer 怎么写）：
  1. 客户端取消该轮调用后，服务端 handler 靠什么感知、多快感知
     （context.add_done_callback / context.cancelled() 轮询 / yield 时抛错）
  2. 一条 ClientConn 上并发多轮 RPC 会不会串行
  3. 取消后服务端还有没有机会做清理（关上游流、写历史）

用法（项目根目录）：
  python agent/test/spike_cancel.py

这是一次性诊断脚本，结论固化进 agent/server/turn_service.py 与测试后可删
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

# 每轮生成的事件数与事件间隔，决定单轮时长（用于观察并发是否串行）
TOKENS_PER_TURN = 60
TOKEN_INTERVAL = 0.05
# 并发轮数
CONCURRENT_TURNS = 20


class SpikeServicer(llm_pb2_grpc.LLMServiceServicer):
    """诊断用 servicer：逐事件产出，并记录各种取消感知路径的时刻"""

    def __init__(self) -> None:
        """初始化，记录每轮的时间线"""
        self.marks: dict[str, dict[str, float]] = {}  # turn_id → 各事件相对开始时刻的毫秒数

    async def Generate(self, request, context):
        """逐事件流式产出，记录取消感知时刻

        request: TurnRequest，含 session_id 与 turn_id
        context: gRPC 调用上下文，注册终止回调并轮询取消状态
        return: TurnResponse 事件流
        """
        t0 = time.monotonic()
        marks: dict[str, float] = {"start": 0.0}
        self.marks[request.turn_id] = marks

        def on_done() -> None:
            """RPC 终止（客户端取消 / deadline / 正常结束）时的回调"""
            marks["done_callback"] = (time.monotonic() - t0) * 1000

        context.add_done_callback(on_done)

        for i in range(TOKENS_PER_TURN):
            if context.cancelled() and "poll_cancelled" not in marks:
                marks["poll_cancelled"] = (time.monotonic() - t0) * 1000
            try:
                yield llm_pb2.TurnResponse(
                    session_id=request.session_id,
                    room_id=request.room_id,
                    client_id=request.client_id,
                    turn_id=request.turn_id,
                    response_text=f"t{i}",
                    is_final=False,
                )
            except Exception as exc:  # noqa: BLE001 诊断用：任何异常都记下来看类型
                marks["yield_raised"] = (time.monotonic() - t0) * 1000
                marks["yield_exc"] = type(exc).__name__  # type: ignore[assignment]
                return
            if i == 0:
                marks["first_token"] = (time.monotonic() - t0) * 1000
            await asyncio.sleep(TOKEN_INTERVAL)

        marks["finished"] = (time.monotonic() - t0) * 1000


async def run_spike() -> None:
    """起真实 aio server，跑并发与取消两组观察，打印结论"""
    servicer = SpikeServicer()
    server = grpc.aio.server()
    llm_pb2_grpc.add_LLMServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = grpc.aio.insecure_channel(f"127.0.0.1:{port}")
    stub = llm_pb2_grpc.LLMServiceStub(channel)

    # ---- 观察一：并发 20 轮，看首事件时延是否随并发数线性增长 ----
    print(f"\n[观察一] 一条连接上并发 {CONCURRENT_TURNS} 轮，单轮 {TOKENS_PER_TURN} 个事件")
    first_token_ms: list[float] = []
    tasks: list[asyncio.Task] = []

    async def one_turn(idx: int) -> None:
        """发起一轮并记录首个事件的到达时刻

        idx: 轮次序号，用于构造 turn_id
        """
        turn_id = f"spike-{idx}"
        call_started = time.monotonic()
        try:
            async for _resp in stub.Generate(
                llm_pb2.TurnRequest(
                    session_id=f"s-{idx}",
                    room_id=f"room-{idx}",
                    client_id=f"c-{idx}",
                    turn_id=turn_id,
                    user_text="你好",
                )
            ):
                first_token_ms.append((time.monotonic() - call_started) * 1000)
                break
        except grpc.aio.AioRpcError as exc:
            print(f"  轮 {turn_id} 异常结束: {exc.code()}")

    for i in range(CONCURRENT_TURNS):
        tasks.append(asyncio.create_task(one_turn(i)))
    await asyncio.gather(*tasks, return_exceptions=True)
    if first_token_ms:
        print(f"  首事件时延: 最小 {min(first_token_ms):.1f}ms  最大 {max(first_token_ms):.1f}ms")
        print(f"  结论: 若最大值接近 {CONCURRENT_TURNS * TOKEN_INTERVAL * 1000:.0f}ms 说明服务端串行处理")

    # ---- 观察二：取消一轮，看服务端多久感知、客户端多久返回 ----
    print("\n[观察二] 取消一轮，看两侧的感知时延")
    cancel_target = "cancel-me"
    cancel_call = stub.Generate(
        llm_pb2.TurnRequest(
            session_id="s-cancel",
            room_id="room-cancel",
            client_id="c-cancel",
            turn_id=cancel_target,
            user_text="你好",
        )
    )
    received = 0
    async for _resp in cancel_call:
        received += 1
        if received == 3:
            break
    cancel_sent_at = time.monotonic()
    cancel_call.cancel()
    try:
        async for _resp in cancel_call:
            pass
    except (grpc.aio.AioRpcError, asyncio.CancelledError):
        pass
    client_returned_ms = (time.monotonic() - cancel_sent_at) * 1000
    await asyncio.sleep(0.6)  # 等服务端把回调与轮询跑完

    marks = servicer.marks.get(cancel_target, {})
    print(f"  客户端取消后本地返回耗时: {client_returned_ms:.1f}ms")
    print(f"  服务端时间线（相对该轮开始，毫秒）: {_fmt(marks)}")
    print("  解读: done_callback 时刻 = 取消回调；poll_cancelled 时刻 = 轮询感知；")
    print("        yield_raised 存在则说明 yield 会抛错，服务端能被动感知")

    await channel.close()
    await server.stop(0)
    print("\n[结论] 见上方时间线，据此决定 turn_service 的取消实现")


def _fmt(marks: dict) -> str:
    """把时间线格式化成一行文本

    marks: 事件名 → 毫秒数（或异常名）的字典
    return: 便于阅读的字符串
    """
    return "  ".join(f"{k}={v if isinstance(v, str) else round(v, 1)}" for k, v in marks.items())


if __name__ == "__main__":
    asyncio.run(run_spike())
