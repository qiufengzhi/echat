"""
gRPC aio 拦截器：日志记录、异常转 status code、超时控制

gRPC aio 的拦截器基于 ServerInterceptor，每个 RPC 进来时包装其 handler
本文件实现了三个：
  LoggingInterceptor：记录每个 RPC 的方法名、耗时
  ErrorHandlingInterceptor：捕获漏出去的异常，映射为 gRPC status code
  TimeoutInterceptor：单次 RPC 超过阈值返回 DEADLINE_EXCEEDED
"""

from __future__ import annotations

import time
from typing import Any, Awaitable, Callable, Optional

import grpc
from loguru import logger

from agent.core.types import ErrorCode
from agent.providers.base import ProviderError


class _HandlerWrapper:
    """包装 RpcMethodHandler，为 stream_stream 方法注入拦截逻辑"""

    def __init__(self, handler, hook: Callable[[Callable], Callable]):
        """初始化 handler 包装器

        handler: 原始 gRPC RpcMethodHandler，保存其方法元数据
        hook: 注入钩子，接收原始双向流方法并返回包装后的方法
        """
        self._handler = handler  # 原始 RpcMethodHandler
        self._hook = hook  # 注入钩子，返回包装后的方法
        self._copy_metadata()

    def _copy_metadata(self) -> None:
        """复制原始 handler 的方法元数据，并把双向流换成注入后的版本"""
        self.request_streaming = self._handler.request_streaming  # 请求是否流式
        self.response_streaming = self._handler.response_streaming  # 响应是否流式
        self.request_deserializer = self._handler.request_deserializer  # 请求反序列化器
        self.response_serializer = self._handler.response_serializer  # 响应序列化器
        self.unary_unary = self._handler.unary_unary  # 一元方法
        self.unary_stream = self._handler.unary_stream  # 服务器流方法
        self.stream_unary = self._handler.stream_unary  # 客户端流方法
        # 双向流在这里包装
        self.stream_stream = self._wrap_stream_stream(self._handler.stream_stream)

    def _wrap_stream_stream(self, inner):
        """对双向流方法应用注入钩子

        inner: 原始双向流方法，可能为 None
        return: 经钩子包装后的方法，原始方法为 None 时返回 None
        """
        if inner is None:
            return None
        return self._hook(inner)


class LoggingInterceptor(grpc.aio.ServerInterceptor):
    """记录每个 RPC 的方法名、耗时"""

    async def intercept_service(self, continuation, handler_call_details):
        """包装 handler，记录调用耗时

        continuation: gRPC 框架的续接回调，用于取得实际 handler
        handler_call_details: 本次调用的细节，含方法名
        return: 包装后的 handler，原始 handler 为 None 时返回 None
        """
        method = handler_call_details.method
        handler = await continuation(handler_call_details)
        if handler is None:
            return None
        start = time.monotonic()

        def hook(inner):
            """返回带计时逻辑的双向流包装

            inner: 原始双向流方法
            return: 带耗时记录的包装方法
            """
            async def wrapped(request_iterator, context):
                """转发流并在结束时记录耗时

                request_iterator: 客户端请求流，原样转发
                context: gRPC 调用上下文，用于响应与状态
                return: 逐项转发的响应流
                """
                try:
                    async for resp in inner(request_iterator, context):
                        yield resp
                finally:
                    elapsed = (time.monotonic() - start) * 1000
                    logger.info("RPC {} 完成 耗时 {:.0f}ms", method, elapsed)
            return wrapped

        return _HandlerWrapper(handler, hook)


def _error_code_to_grpc(code: str) -> grpc.StatusCode:
    """把结构化错误码映射为 gRPC status code

    code: ErrorCode 枚举的字符串值
    return: 对应的 gRPC status code，未知错误码归为 UNKNOWN
    """
    mapping = {
        ErrorCode.LLM_AUTH_ERROR.value: grpc.StatusCode.UNAUTHENTICATED,
        ErrorCode.LLM_RATE_LIMITED.value: grpc.StatusCode.RESOURCE_EXHAUSTED,
        ErrorCode.LLM_TIMEOUT.value: grpc.StatusCode.DEADLINE_EXCEEDED,
        ErrorCode.LLM_API_ERROR.value: grpc.StatusCode.INTERNAL,
        ErrorCode.SESSION_NOT_FOUND.value: grpc.StatusCode.NOT_FOUND,
        ErrorCode.SESSION_POOL_FULL.value: grpc.StatusCode.RESOURCE_EXHAUSTED,
        ErrorCode.INTERNAL_ERROR.value: grpc.StatusCode.INTERNAL,
    }
    return mapping.get(code, grpc.StatusCode.UNKNOWN)


class ErrorHandlingInterceptor(grpc.aio.ServerInterceptor):
    """捕获所有漏出去的异常，映射为 gRPC status code，防止 stream 断开"""

    async def intercept_service(self, continuation, handler_call_details):
        """包装 handler，捕获异常并设置 gRPC status code

        continuation: gRPC 框架的续接回调，用于取得实际 handler
        handler_call_details: 本次调用的细节，含方法名
        return: 包装后的 handler，原始 handler 为 None 时返回 None
        """
        method = handler_call_details.method
        handler = await continuation(handler_call_details)
        if handler is None:
            return None

        def hook(inner):
            """返回带异常捕获逻辑的双向流包装

            inner: 原始双向流方法
            return: 带异常捕获的包装方法
            """
            async def wrapped(request_iterator, context):
                """转发流，捕获 ProviderError 和未知异常

                request_iterator: 客户端请求流，原样转发
                context: gRPC 调用上下文，异常时设置 status code 与详情
                return: 逐项转发的响应流
                """
                try:
                    async for resp in inner(request_iterator, context):
                        yield resp
                except ProviderError as exc:
                    logger.exception("ProviderError RPC={}", method)
                    context.set_code(_error_code_to_grpc(exc.code))
                    context.set_details(exc.message)
                except Exception:
                    logger.exception("未处理异常 RPC={}", method)
                    context.set_code(grpc.StatusCode.INTERNAL)
                    context.set_details("内部错误")
            return wrapped

        return _HandlerWrapper(handler, hook)


class TimeoutInterceptor(grpc.aio.ServerInterceptor):
    """单次 RPC 超过 timeout_seconds 返回 DEADLINE_EXCEEDED

    ChatStream 是长连接双向流，硬性超时会中断对话，因此本拦截器
    在 cmd/main.py 中默认不启用（传 timeout_seconds=None），需要时显式开启
    """

    def __init__(self, timeout_seconds: Optional[float] = None) -> None:
        """初始化超时拦截器

        timeout_seconds: 单次 RPC 超时秒数，None 表示不启用超时限制
        """
        self._timeout = timeout_seconds  # 超时秒数，None 表示不启用

    async def intercept_service(self, continuation, handler_call_details):
        """包装 handler，逐项检查剩余时间

        continuation: gRPC 框架的续接回调，用于取得实际 handler
        handler_call_details: 本次调用的细节
        return: 原样或带超时包装的 handler，超时未启用时原样返回
        """
        handler = await continuation(handler_call_details)
        if handler is None or self._timeout is None:
            return handler

        import asyncio

        timeout = self._timeout

        def hook(inner):
            """返回带超时判断的双向流包装

            inner: 原始双向流方法
            return: 带超时判断的包装方法
            """
            async def wrapped(request_iterator, context):
                """转发流，超时返回 DEADLINE_EXCEEDED

                request_iterator: 客户端请求流，原样转发
                context: gRPC 调用上下文，超时或取项超时设置 DEADLINE_EXCEEDED
                return: 逐项转发的响应流
                """
                loop = asyncio.get_event_loop()
                it = inner(request_iterator, context)
                deadline = loop.time() + timeout
                while True:
                    remaining = deadline - loop.time()
                    if remaining <= 0:
                        context.set_code(grpc.StatusCode.DEADLINE_EXCEEDED)
                        context.set_details("请求超时")
                        return
                    try:
                        # 每次取下一项时检查剩余时间，兼容 3.8（无 asyncio.timeout）
                        item = await asyncio.wait_for(it.__anext__(), remaining)
                    except StopAsyncIteration:
                        return
                    except asyncio.TimeoutError:
                        context.set_code(grpc.StatusCode.DEADLINE_EXCEEDED)
                        context.set_details("请求超时")
                        return
                    yield item
            return wrapped

        return _HandlerWrapper(handler, hook)
