// http.go 认证域 HTTP 出口：注册 / 邮箱验证 的 JSON 接口
package authn

import (
	"encoding/json"
	"net"
	"net/http"
)

// Handler 认证域 HTTP 处理器，方法按 Go 1.22 增强的 ServeMux 路由注册
type Handler struct {
	// svc 底层认证服务
	svc *Service
}

// NewHandler 构造认证域 HTTP 处理器
// svc 认证服务实例
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register POST /api/v1/auth/register 注册接口
// 请求体 {username, password, email?}，本地账号即生效，邮箱账号返回 need_verify=true
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	result, err := h.svc.Register(r.Context(), req)
	if err != nil {
		writeError(w, toError(err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Verify POST /api/v1/auth/verify 邮箱验证接口
// 请求体 {token}，覆盖注册激活与补绑邮箱两种一次性链接
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Token 一次性验证令牌，来自邮件链接 query 参数
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	if err := h.svc.VerifyEmail(r.Context(), req.Token); err != nil {
		writeError(w, toError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// readJSON 读取并解析 JSON 请求体到 out，失败返回 ErrBadRequest
func readJSON(r *http.Request, out any) error {
	// 限制 1 MiB 上限，避免超大请求体拖垮服务
	const maxBody = 1 << 20
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		return ErrBadRequest
	}
	return nil
}

// refreshCookieName refresh token 的 httpOnly cookie 名
const refreshCookieName = "refresh_token"

// Login POST /api/v1/auth/login 登录接口
// 请求体 {identifier, password}；成功后签发双 token，refresh 写入 httpOnly cookie
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	result, err := h.svc.Login(r.Context(), req, clientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, toError(err))
		return
	}
	setRefreshCookie(w, r, result.RefreshToken)
	writeJSON(w, http.StatusOK, result)
}

// Refresh POST /api/v1/auth/refresh 轮换刷新
// refresh 从 cookie 或请求体读取，成功后换发新 refresh（继续写 cookie）
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	rt := h.refreshTokenFrom(r)
	result, err := h.svc.Refresh(r.Context(), rt, clientIP(r))
	if err != nil {
		writeError(w, toError(err))
		return
	}
	setRefreshCookie(w, r, result.RefreshToken)
	writeJSON(w, http.StatusOK, result)
}

// Logout POST /api/v1/auth/logout 单设备退出：吊销当前 refresh 会话
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), h.refreshTokenFrom(r)); err != nil {
		writeError(w, toError(err))
		return
	}
	clearRefreshCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// LogoutAll POST /api/v1/auth/logout-all 全设备退出
// 需要 Authorization: Bearer access；吊销全部会话并 +1 token_version，实时连接随之被踢
func (h *Handler) LogoutAll(w http.ResponseWriter, r *http.Request, claims *AccessClaims) {
	if err := h.svc.LogoutAll(r.Context(), claims.SubjectUUID()); err != nil {
		writeError(w, toError(err))
		return
	}
	clearRefreshCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PasswordResetRequest POST /api/v1/auth/password/reset-request 申请重置
// 无论邮箱是否存在都返回 ok，防枚举（spec §6.1）
func (h *Handler) PasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	var req PasswordResetRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	if err := h.svc.RequestPasswordReset(r.Context(), req); err != nil {
		writeError(w, toError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PasswordReset POST /api/v1/auth/password/reset 令牌重置
func (h *Handler) PasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Token 一次性重置令牌
		Token string `json:"token"`
		// NewPassword 新密码
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	if err := h.svc.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		writeError(w, toError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// EmailBind POST /api/v1/auth/email/verify-request 申请绑定/换绑邮箱
// 需要 Authorization: Bearer access；签发一次性验证链接并发邮件
func (h *Handler) EmailBind(w http.ResponseWriter, r *http.Request, claims *AccessClaims) {
	var req BindEmailRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	if err := h.svc.RequestEmailBind(r.Context(), claims.SubjectUUID(), req); err != nil {
		writeError(w, toError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PasswordChange POST /api/v1/auth/password/change 已登录改密
// 需要 Authorization: Bearer access；将吊销本设备之外全部会话
func (h *Handler) PasswordChange(w http.ResponseWriter, r *http.Request, claims *AccessClaims) {
	var req ChangePasswordRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, toError(err))
		return
	}
	if err := h.svc.ChangePassword(r.Context(), claims.SubjectUUID(), claims.SessionID, req); err != nil {
		writeError(w, toError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// refreshTokenFrom 优先读取 httpOnly cookie，其次请求体（供纯 API 客户端）
func (h *Handler) refreshTokenFrom(r *http.Request) string {
	if c, err := r.Cookie(refreshCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	var body struct {
		// RefreshToken 明文刷新串
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.RefreshToken != "" {
		return body.RefreshToken
	}
	return ""
}

// clientIP 提取来源 IP：优先 RemoteAddr 的 host 部分，退化返回原串
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// setRefreshCookie 写 refresh 的 httpOnly cookie：防 XSS 窃取；SameSite 防 CSRF；仅限 auth 路径
func setRefreshCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    value,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil, // 开发 http 下不强制 Secure，生产 https 自动开启
	})
}

// clearRefreshCookie 清除 refresh cookie（登出）
func clearRefreshCookie(w http.ResponseWriter, r *http.Request) {
	setRefreshCookie(w, r, "")
}

// errorEnvelope 对外错误响应结构：统一包一层 error
type errorEnvelope struct {
	// Error 错误对象，含 code 与 message
	Error errorBody `json:"error"`
}

// errorBody 错误主体
type errorBody struct {
	// Code 错误码，前端可据此分支处理
	Code string `json:"code"`
	// Message 泛化文案，面向用户展示
	Message string `json:"message"`
}

// writeJSON 以 application/json 写成功响应
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 把错误写为统一错误包络响应
func writeError(w http.ResponseWriter, ae *Error) {
	if ae == nil {
		ae = ErrInternal
	}
	status := ae.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{Code: ae.Code, Message: ae.Message}})
}
