"""
TurnServiceServicer：每轮一次 RPC 的对话服务实现

一轮对话一次 Generate 调用：请求一次，响应逐事件流式返回
取消由客户端取消该轮调用表达：grpc.aio 会直接取消本 handler 所在 task，
CancelledError 在当前 await 点抛出，因此靠异常传播 + try/finally 清理，
不依赖 context.add_done_callback（实测在 grpc 1.81.1 上不会触发）
超时由 RPC deadline 表达，与取消同一条路径

同一会话的轮次串行处理（会话内不能并行：按轮写回 session.history），
不同会话并行处理
"""

from __future__ import annotations

import asyncio
from typing import AsyncIterator, Dict, Optional

import grpc
from loguru import logger

from agent.core.orchestrator import ChatOrchestrator
from agent.core.types import (
    DoneEvent,
    ErrorEvent,
    StreamEvent,
    TokenEvent,
    ToolCallEvent,
    ToolResultEvent,
)
from agent.pb import llm_pb2, llm_pb2_grpc


class TurnServiceServicer(llm_pb2_grpc.LLMServiceServicer):
    """gRPC 服务实现：每轮一次 RPC + 会话释放 + 健康检查"""

    def __init__(self, orchestrator: ChatOrchestrator, health_service=None) -> None:
        """初始化服务实现

        orchestrator: 编排引擎，处理单轮对话与会话释放
        health_service: 独立健康服务，可为空，为空时健康检查兜底返回 SERVING
        """
        self._orchestrator = orchestrator  # 编排引擎
        self._health_service = health_service  # 健康服务，可为空
        self._session_locks: Dict[str, asyncio.Lock] = {}  # 会话 ID -> 串行锁
        self._pool_lock = asyncio.Lock()  # 保护 _session_locks 的增删

    # ------------------------------------------------------------------
    # 会话串行化
    # ------------------------------------------------------------------
    async def _session_lock(self, session_id: str) -> asyncio.Lock:
        """取会话的串行锁，不存在则创建

        session_id: 会话 ID
        return: 该会话的锁对象，同一会话的轮次在该锁上排队
        """
        async with self._pool_lock:
            lock = self._session_locks.get(session_id)
            if lock is None:
                lock = asyncio.Lock()
                self._session_locks[session_id] = lock
            return lock

    async def _forget_lock(self, session_id: str) -> None:
        """在会话释放时丢弃其锁

        仅在该锁未被持有时丢弃，避免与仍在收尾的轮次抢同一把锁
        session_id: 会话 ID
        """
        async with self._pool_lock:
            lock = self._session_locks.get(session_id)
            if lock is not None and not lock.locked():
                del self._session_locks[session_id]

    # ------------------------------------------------------------------
    # RPC 实现
    # ------------------------------------------------------------------
    async def Generate(
        self,
        request: llm_pb2.TurnRequest,
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[llm_pb2.TurnResponse]:
        """处理一轮对话：取会话锁 → 串行执行 → 逐事件返回

        request: 轮次请求，含会话、房间、用户文本与轮次 ID
        context: gRPC 调用上下文，本轮被取消或超时时本处理函数所在 task 被取消
        return: TurnResponse 事件流，同一轮的所有事件共享 turn_id
        """
        session_id = request.session_id
        user_text = request.user_text.strip() if request.user_text else ""
        # 上一轮被打断时用户实际听到的句前缀：仅在请求显式携带时转发，
        # 空串是合法取值（表示什么都没播出，半句应从历史移除）
        played_prefix = (
            request.played_reply_prefix
            if request.HasField("played_reply_prefix")
            else None
        )

        # 空文本或缺失会话：直接回结束事件，避免调用方等到超时
        if not user_text or not session_id:
            yield llm_pb2.TurnResponse(
                session_id=session_id,
                room_id=request.room_id,
                client_id=request.client_id,
                turn_id=request.turn_id,
                seq=request.seq,
                is_final=True,
            )
            return

        lock = await self._session_lock(session_id)
        async with lock:
            saw_final = False
            try:
                async for event in self._orchestrator.handle_message(
                    session_id,
                    user_text,
                    room_id=request.room_id,
                    played_reply_prefix=played_prefix,
                ):
                    resp = self._event_to_response(event, request)
                    if resp.is_final:
                        saw_final = True
                    yield resp
            except asyncio.CancelledError:
                # 客户端取消或 deadline：本轮立即停止，向上抛出交给 grpc 记状态
                logger.info("轮次已取消 session={} turn={}", session_id, request.turn_id)
                raise
            except Exception:
                logger.exception("轮次异常 session={} turn={}", session_id, request.turn_id)
                yield llm_pb2.TurnResponse(
                    session_id=session_id,
                    room_id=request.room_id,
                    client_id=request.client_id,
                    turn_id=request.turn_id,
                    seq=request.seq,
                    error_code="INTERNAL_ERROR",
                    response_text="[服务异常]",
                    is_final=True,
                )
                return

            # 兜底补一个结束事件：调用方按 is_final 冲刷 TTS 缓冲并清理会话，
            # 缺了它会让最后一段文本永远停在缓冲里
            if not saw_final:
                yield llm_pb2.TurnResponse(
                    session_id=session_id,
                    room_id=request.room_id,
                    client_id=request.client_id,
                    turn_id=request.turn_id,
                    seq=request.seq,
                    is_final=True,
                )

    async def CloseSession(
        self,
        request: llm_pb2.CloseSessionRequest,
        context: grpc.aio.ServicerContext,
    ) -> llm_pb2.CloseSessionResponse:
        """释放会话：丢弃会话锁并让编排引擎释放会话（取消在途生成、保存历史）

        request: 释放请求，含会话 ID 与原因
        context: gRPC 调用上下文
        return: 释放结果，closed 表示会话是否存在并被释放
        """
        await self._forget_lock(request.session_id)
        closed = await self._orchestrator.close_session(request.session_id)
        logger.info(
            "释放会话 session={} reason={} closed={}",
            request.session_id,
            request.reason,
            closed,
        )
        return llm_pb2.CloseSessionResponse(closed=closed)

    async def HealthCheck(
        self,
        request: llm_pb2.HealthCheckRequest,
        context: grpc.aio.ServicerContext,
    ) -> llm_pb2.HealthCheckResponse:
        """委托健康检查给独立服务，无健康服务时兜底返回 SERVING

        request: 健康检查请求，proto 定义
        context: gRPC 调用上下文，可用于取消与元数据
        return: 健康状态响应，含服务状态与运行指标
        """
        if self._health_service is not None:
            return await self._health_service.HealthCheck(request, context)
        return llm_pb2.HealthCheckResponse(status=llm_pb2.HealthCheckResponse.SERVING)

    # ------------------------------------------------------------------
    # proto ↔ StreamEvent 转换
    # ------------------------------------------------------------------
    def _event_to_response(
        self,
        event: StreamEvent,
        request: llm_pb2.TurnRequest,
    ) -> llm_pb2.TurnResponse:
        """把 StreamEvent 转换成对应的 TurnResponse 字段

        event: 编排引擎产出的事件对象，类型决定响应填充哪些字段
        request: 本轮请求，公共字段（会话、房间、客户端、轮次、序号）由它回填
        return: 填好事件相关字段的 TurnResponse
        """
        base = llm_pb2.TurnResponse(
            session_id=request.session_id,
            room_id=request.room_id,
            client_id=request.client_id,
            turn_id=request.turn_id,
            seq=request.seq,
        )
        if isinstance(event, TokenEvent):
            base.response_text = event.content
            base.is_final = False
        elif isinstance(event, ToolCallEvent):
            base.event_type = "tool_call"
            base.tool_call_id = event.tool_call_id
            base.tool_name = event.tool_name
            base.tool_arguments = event.arguments
            base.is_final = False
        elif isinstance(event, ToolResultEvent):
            base.event_type = "tool_result"
            base.tool_call_id = event.tool_call_id
            base.tool_name = event.name
            base.tool_result = event.result
            base.is_final = False
        elif isinstance(event, ErrorEvent):
            base.error_code = event.code
            base.response_text = event.message
            base.is_final = True
        elif isinstance(event, DoneEvent):
            base.is_final = True
        else:
            base.is_final = False
        return base
