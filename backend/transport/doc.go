// Package transport 提供 HTTP 传输基元：统一 handler 签名、错误包络、JSON 读写、中间件链
//
// 定位：补齐标准库 net/http + Go 1.22 ServeMux 的通用传输能力，不做成 web 框架
// 业务 handler 统一签名 transport.HandlerFunc（成功写响应，失败返回 error），由 Adapt 适配为标准 handler
// 领域错误统一实现 error 接口并携带 Code/Message/Status，由 WriteError 序列化为 {error:{code,message}} 包络
// 中间件链（Recover/RequestID 等）在此组合，保证单请求 panic 与日志串联行为一致
package transport
