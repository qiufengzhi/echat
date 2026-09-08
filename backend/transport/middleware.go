package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"echat-backend/logging"
)

// requestIDKey context 中存放请求 ID 的键类型，避免与其他键冲突
type requestIDKey struct{}

// Middleware 中间件签名，接受并返回 http.Handler
type Middleware func(http.Handler) http.Handler

// Chain 按给定顺序组合中间件，返回最外层 handler
// 执行顺序：先注册的中间件越靠外，最后执行，最先看到请求
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Recover 捕获 panic 转为 500，避免单请求 panic 打崩服务
// panic 已写入日志后按内部错误响应，未知 panic 不再向上传播
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logging.L().Warnw("http handler panic",
					"path", r.URL.Path, "request_id", RequestIDFrom(r.Context()), "panic", rec)
				WriteError(w, &Error{Code: "INTERNAL_ERROR", Message: "服务器开小差了，请稍后再试", Status: http.StatusInternalServerError})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequestID 注入或透传请求 ID 到 context 与响应头，便于日志串联
// 优先沿用客户端 X-Request-ID（幂等重试场景需要同一请求 ID），缺失时服务端生成
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFrom 从 context 取出请求 ID，未注入时返回空串
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID 生成 16 字节安全随机的请求 ID（十六进制 32 位）
func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(buf)
}
