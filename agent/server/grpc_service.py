"""
LLMServiceServicer：实现 proto 定义的 ChatStream 双向流 RPC

这一层只做"翻译"：gRPC 双向流里的 LLMRequest（proto）→ orchestrator 的
handle_message() → LLMResponse（proto），不拼 messages、不调 LLM API、
不管 session 生命周期
"""

from __future__ import annotations

from typing import AsyncIterator

import grpc
from loguru import logger

from agent.core.types import (
    DoneEvent,
    ErrorEvent,
    StreamEvent,
    TokenEvent,
    ToolCallEvent,
    ToolResultEvent,
)
from agent.pb import llm_pb2, llm_pb2_grpc


class LLMServiceServicer(llm_pb2_grpc.LLMServiceServicer):
    """gRPC 服务实现：双向流聊天 + 健康检查

    HealthCheck 委托给独立的 HealthService（保持 health.py 职责单一）
    """

    def __init__(self, orchestrator, health_service=None) -> None:
        """初始化 gRPC 服务实现

        orchestrator: 编排引擎，处理聊天与取消请求
        health_service: 独立健康服务，可为空；为空时健康检查兜底返回 SERVING
        """
        self._orchestrator = orchestrator  # 编排引擎，处理聊天请求
        self._health_service = health_service  # 健康服务，可为空

    async def HealthCheck(
        self,
        request: llm_pb2.HealthCheckRequest,
        context: grpc.aio.ServicerContext,
    ) -> llm_pb2.HealthCheckResponse:
        """委托健康检查给独立服务，无 health service 时兜底返回 SERVING

        request: 健康检查请求，proto 定义
        context: gRPC 调用上下文，可用于取消与元数据
        return: 健康状态响应，含服务状态与运行指标
        """
        if self._health_service is not None:
            return await self._health_service.HealthCheck(request, context)
        return llm_pb2.HealthCheckResponse(status=llm_pb2.HealthCheckResponse.SERVING)

    async def ChatStream(
        self,
        request_iterator: AsyncIterator[llm_pb2.LLMRequest],
        context: grpc.aio.ServicerContext,
    ) -> AsyncIterator[llm_pb2.LLMResponse]:
        """双向流式聊天 RPC：收 proto → 调 core → 回 proto

        request_iterator: 客户端推入的 LLMRequest 流，含会话 ID、文本、取消信号等
        context: gRPC 调用上下文，可用于取消与元数据
        return: LLMResponse 响应流，含文本、工具调用、错误码与结束标记
        """
        session_id = ""
        room_id = ""

        async for request in request_iterator:
            session_id = request.session_id
            room_id = request.room_id or room_id
            client_id = request.client_id
            seq = request.seq
            user_text = request.user_text.strip() if request.user_text else ""
            cancel = request.cancel

            # 1. 取消信号 → 中断该会话正在生成的回复
            if cancel:
                await self._orchestrator.cancel(session_id)
                yield llm_pb2.LLMResponse(
                    session_id=session_id,
                    room_id=room_id,
                    client_id=client_id,
                    response_text="",
                    is_final=True,
                    seq=seq,
                )
                continue

            # 2. 空文本或缺失 session → 空回复占位
            if not user_text or not session_id:
                yield llm_pb2.LLMResponse(
                    session_id=session_id,
                    room_id=room_id,
                    client_id=client_id,
                    response_text="",
                    is_final=True,
                    seq=seq,
                )
                continue

            # 3. 调 orchestrator，把 StreamEvent 逐个转回 LLMResponse
            try:
                async for event in self._orchestrator.handle_message(
                    session_id, user_text, room_id=room_id
                ):
                    resp = self._event_to_response(event, session_id, room_id, client_id, seq)
                    yield resp
            except Exception:
                logger.exception("聊天流异常 session={}", session_id)
                yield llm_pb2.LLMResponse(
                    session_id=session_id,
                    room_id=room_id,
                    client_id=client_id,
                    error_code="INTERNAL_ERROR",
                    response_text="[服务异常]",
                    is_final=True,
                    seq=seq,
                )

    # ------------------------------------------------------------------
    # proto ↔ StreamEvent 转换
    # ------------------------------------------------------------------
    def _event_to_response(
        self,
        event: StreamEvent,
        session_id: str,
        room_id: str,
        client_id: str,
        seq: int,
    ) -> llm_pb2.LLMResponse:
        """把 StreamEvent 转换成对应的 LLMResponse 字段

        event: 编排引擎产出的事件对象，类型决定响应填充哪些字段
        session_id: 会话标识，原样回填到响应
        room_id: 房间标识，原样回填到响应
        client_id: 客户端标识，原样回填到响应
        seq: 消息序号，原样回填到响应
        return: 填好事件相关字段的 LLMResponse，公共字段取入参
        """
        base = llm_pb2.LLMResponse(
            session_id=session_id,
            room_id=room_id,
            client_id=client_id,
            seq=seq,
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
