"""HealthCheck RPC：返回服务健康状态（unary，非流式）"""

from __future__ import annotations

import time

import grpc

from agent.pb import llm_pb2

# ServingStatus 映射：内部字符串 → proto 枚举
_STATUS_TO_PROTO = {
    "SERVING": llm_pb2.HealthCheckResponse.SERVING,
    "DEGRADED": llm_pb2.HealthCheckResponse.DEGRADED,
    "NOT_SERVING": llm_pb2.HealthCheckResponse.NOT_SERVING,
}


class HealthService:
    """实现 HealthCheck RPC，聚合 orchestrator 的健康状态"""

    def __init__(self, orchestrator) -> None:
        """初始化健康服务

        orchestrator: 编排引擎，提供健康状态聚合数据
        """
        self._orchestrator = orchestrator  # 编排引擎，提供健康数据
        self._start_time = time.time()  # 服务启动时间戳

    async def HealthCheck(
        self,
        request: llm_pb2.HealthCheckRequest,
        context: grpc.aio.ServicerContext,
    ) -> llm_pb2.HealthCheckResponse:
        """聚合健康状态并转成 proto 响应

        request: 健康检查请求，proto 定义，含被检查服务名
        context: gRPC 调用上下文，可用于取消与元数据
        return: 健康状态响应，含服务状态、会话数、提供商与运行时长
        """
        status = await self._orchestrator.health()
        proto_status = _STATUS_TO_PROTO.get(status.status, llm_pb2.HealthCheckResponse.UNKNOWN)
        return llm_pb2.HealthCheckResponse(
            status=proto_status,
            active_sessions=status.active_sessions,
            provider_name=status.provider_name,
            uptime_seconds=status.uptime_seconds,
        )
