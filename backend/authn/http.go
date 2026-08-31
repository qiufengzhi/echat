// http.go 认证域 HTTP 出口：注册 / 邮箱验证 的 JSON 接口
package authn

import (
	"encoding/json"
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
