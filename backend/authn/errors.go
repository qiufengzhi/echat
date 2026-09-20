// Package authn 实现用户系统认证域：注册、邮箱验证/绑定、登录会话、找回密码
//
// 本包只做「认证」；「授权（房间角色等）」在 authz 域
// 领域错误统一复用 transport.Error（Code/Message/Status 三要素），便于 http.go 直接经 transport.Adapt 序列化
package authn

import (
	"net/http"

	"echat-backend/transport"
)

// Error 认证域错误别名，Code 供前端分支，Message 面向用户一律泛化（防枚举）
type Error = transport.Error

// 预定义错误：message 均为泛化文案，避免成为账户探测的侧信道
var (
	// ErrBadRequest 请求体/参数校验失败
	ErrBadRequest = &Error{Code: "VALIDATION_ERROR", Message: "请求参数不合法", Status: http.StatusBadRequest}
	// ErrConflict 注册冲突（用户名/邮箱已占用），统一泛化
	ErrConflict = &Error{Code: "REGISTER_CONFLICT", Message: "注册信息无法完成", Status: http.StatusConflict}
	// ErrWeakPassword 密码不满足长度要求（>= 8 位）
	ErrWeakPassword = &Error{Code: "WEAK_PASSWORD", Message: "密码至少 8 位", Status: http.StatusBadRequest}
	// ErrInvalidToken 一次性令牌无效/过期/已消费
	ErrInvalidToken = &Error{Code: "TOKEN_INVALID", Message: "验证链接无效或已过期", Status: http.StatusBadRequest}
	// ErrAccessTokenInvalid access token 无效/过期/被吊销（HTTP 中间件与 WS 握手共用，401 让客户端走刷新）
	ErrAccessTokenInvalid = &Error{Code: "ACCESS_TOKEN_INVALID", Message: "登录已失效，请重新登录", Status: http.StatusUnauthorized}
	// ErrInvalidCredentials 用户名/密码错误，均返回同一文案防枚举
	ErrInvalidCredentials = &Error{Code: "INVALID_CREDENTIALS", Message: "用户名或密码错误", Status: http.StatusUnauthorized}
	// ErrAccountSuspended 账户被封禁，仅提示联系客服，不泄露具体处置
	ErrAccountSuspended = &Error{Code: "ACCOUNT_SUSPENDED", Message: "账户状态异常，请联系客服处理", Status: http.StatusForbidden}
	// ErrInternal 服务端内部错误，只给泛化文案
	ErrInternal = &Error{Code: "INTERNAL_ERROR", Message: "服务器开小差了，请稍后再试", Status: http.StatusInternalServerError}
)

// toError 把任意 error 归一为对外可写出的 *Error：未知错误一律转内部错误
// err 可能是 *Error，也可能来自底层存储/系统
func toError(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Code: ErrInternal.Code, Message: ErrInternal.Message, Status: ErrInternal.Status}
}
