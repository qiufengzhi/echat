"""
HTTP 调试接口：通过 POST /api/v1/chat 测试大模型的完整对话链路

与 gRPC ChatStream 共用同一个 orchestrator 实例，走完全相同的链路：
session + 历史 + 工具 + 策略。支持两种响应方式：
  - stream=true  → SSE（text/event-stream）逐事件推送
  - stream=false → 一次性 JSON，聚合完整回复和事件列表

用法：
  curl -X POST http://localhost:40001/api/v1/chat \
       -H "Content-Type: application/json" \
       -d '{"session_id":"test-001","text":"你好","stream":true}'
"""

from __future__ import annotations

import json
from functools import partial
from typing import Dict, Optional

import aiohttp.web
from loguru import logger

from agent.core.orchestrator import ChatOrchestrator
from agent.core.types import (
    DoneEvent,
    ErrorEvent,
    StreamEvent,
    TokenEvent,
    TokenUsage,
    ToolCallEvent,
    ToolResultEvent,
)


def _event_to_dict(event: StreamEvent) -> Dict[str, Any]:
    """把 StreamEvent 序列化为 SSE / JSON 用的字典"""
    if isinstance(event, TokenEvent):
        return {"type": "token", "content": event.content}
    if isinstance(event, ToolCallEvent):
        return {
            "type": "tool_call",
            "tool_call_id": event.tool_call_id,
            "tool_name": event.tool_name,
            "arguments": event.arguments,
        }
    if isinstance(event, ToolResultEvent):
        return {
            "type": "tool_result",
            "tool_call_id": event.tool_call_id,
            "name": event.name,
            "result": event.result,
            "is_error": event.is_error,
        }
    if isinstance(event, ErrorEvent):
        return {
            "type": "error",
            "code": event.code,
            "message": event.message,
            "recoverable": event.recoverable,
        }
    if isinstance(event, DoneEvent):
        return {
            "type": "done",
            "cancelled": event.cancelled,
            "finish_reason": event.finish_reason,
            "usage": _usage_to_dict(event.usage),
        }
    return {"type": "unknown"}


def _usage_to_dict(usage: Optional[TokenUsage]) -> Optional[Dict[str, int]]:
    """把 TokenUsage 转成字典，无用时返回 None"""
    if usage is None:
        return None
    return {
        "prompt_tokens": usage.prompt_tokens,
        "completion_tokens": usage.completion_tokens,
        "total_tokens": usage.total_tokens,
    }


# aiohttp 的 json_response 默认用 json.dumps（ensure_ascii=True），这里显式保留中文
_JSON_DUMPS = partial(json.dumps, ensure_ascii=False)


def _json_error(status: int, code: str, message: str) -> aiohttp.web.Response:
    """构造 JSON 错误响应"""
    return aiohttp.web.json_response(
        {"error": {"code": code, "message": message}},
        status=status,
        dumps=_JSON_DUMPS,
    )


class HttpDebugHandler:
    """HTTP 调试接口 handler，持有与 gRPC 共用的 orchestrator"""

    def __init__(self, orchestrator: ChatOrchestrator) -> None:
        self._orchestrator = orchestrator  # 编排引擎，与 gRPC 共用同一实例

    async def handle_chat(self, request: aiohttp.web.Request) -> aiohttp.web.StreamResponse:
        """
        POST /api/v1/chat：完整对话链路测试，支持 SSE 流式或一次性 JSON

        请求体 JSON 参数：
          session_id  必填  string  会话 ID，同一 ID 多次请求保持对话上下文，仅允许 [a-zA-Z0-9_-]
          room_id     选填  string  所属房间 ID，语音场景由上游传入，调试可不填
          text        必填  string  用户消息，去首尾空白后不能为空
          stream      选填  bool    默认 true，true 用 SSE 逐 token 推送，false 返回一次性 JSON
        """
        try:
            body = await request.json()
        except Exception:
            return _json_error(400, "INVALID_JSON", "请求体必须是合法 JSON")

        session_id = body.get("session_id") or ""
        room_id = body.get("room_id") or ""
        text = body.get("text") or ""
        stream = bool(body.get("stream", True))

        if not isinstance(session_id, str) or not isinstance(text, str):
            return _json_error(400, "BAD_REQUEST", "session_id 和 text 必须是字符串")

        text = text.strip()
        if not session_id or not text:
            return _json_error(400, "BAD_REQUEST", "session_id 和 text 为必填字段")

        if stream:
            return await self._stream_response(request, session_id, room_id, text)
        return await self._json_response(session_id, room_id, text)

    async def _stream_response(
        self,
        request: aiohttp.web.Request,
        session_id: str,
        room_id: str,
        text: str,
    ) -> aiohttp.web.StreamResponse:
        """SSE 流式响应，把 orchestrator 事件逐个推给客户端"""
        response = aiohttp.web.StreamResponse(
            status=200,
            headers={
                "Content-Type": "text/event-stream",
                "Cache-Control": "no-cache",
                "Connection": "keep-alive",
                "X-Accel-Buffering": "no",
            },
        )
        await response.prepare(request)
        try:
            async for event in self._orchestrator.handle_message(session_id, text, room_id=room_id):
                payload = json.dumps(_event_to_dict(event), ensure_ascii=False)
                await response.write(("data: " + payload + "\n\n").encode("utf-8"))
        except ConnectionError:
            logger.info("HTTP 客户端断开连接 session={}", session_id)
        except Exception:
            logger.exception("HTTP 聊天流异常 session={}", session_id)
            err = json.dumps(
                {"type": "error", "code": "INTERNAL_ERROR", "message": "内部错误"},
                ensure_ascii=False,
            )
            try:
                await response.write(("data: " + err + "\n\n").encode("utf-8"))
            except Exception:
                pass
        finally:
            await response.write_eof()
        return response

    async def _json_response(
        self,
        session_id: str,
        room_id: str,
        text: str,
    ) -> aiohttp.web.Response:
        """一次性 JSON 响应，聚合完整回复和事件列表"""
        events = []
        reply_parts = []
        usage = None
        error = None
        try:
            async for event in self._orchestrator.handle_message(session_id, text, room_id=room_id):
                if isinstance(event, TokenEvent):
                    reply_parts.append(event.content)
                elif isinstance(event, ErrorEvent):
                    error = {"code": event.code, "message": event.message}
                elif isinstance(event, DoneEvent):
                    usage = event.usage
                events.append(_event_to_dict(event))
        except Exception:
            logger.exception("HTTP 聊天异常 session={}", session_id)
            error = {"code": "INTERNAL_ERROR", "message": "内部错误"}

        return aiohttp.web.json_response(
            {
                "session_id": session_id,
                "reply": "".join(reply_parts),
                "events": events,
                "usage": _usage_to_dict(usage),
                "error": error,
            },
            dumps=_JSON_DUMPS,
        )


def build_http_app(orchestrator: ChatOrchestrator) -> aiohttp.web.Application:
    """构建 aiohttp 调试应用，注册 /api/v1/chat 端点"""
    handler = HttpDebugHandler(orchestrator)
    app = aiohttp.web.Application()
    app.router.add_post("/api/v1/chat", handler.handle_chat)
    return app
