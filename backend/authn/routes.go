package authn

import (
	"net/http"

	"echat-backend/transport"
)

// Register 认证域路由注册，挂载到 App 持有的统一 mux
// 受保护端点走 AccessRequired（中间件注入 claims），其余直接 Adapt 即可
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", transport.Adapt(h.handleRegister))
	mux.HandleFunc("POST /api/v1/auth/verify", transport.Adapt(h.Verify))
	mux.HandleFunc("POST /api/v1/auth/login", transport.Adapt(h.Login))
	mux.HandleFunc("POST /api/v1/auth/refresh", transport.Adapt(h.Refresh))
	mux.HandleFunc("POST /api/v1/auth/logout", transport.Adapt(h.Logout))
	mux.HandleFunc("POST /api/v1/auth/logout-all", transport.Adapt(h.AccessRequired(h.LogoutAll)))
	mux.HandleFunc("POST /api/v1/auth/email/verify-request", transport.Adapt(h.AccessRequired(h.EmailBind)))
	mux.HandleFunc("POST /api/v1/auth/password/reset-request", transport.Adapt(h.PasswordResetRequest))
	mux.HandleFunc("POST /api/v1/auth/password/reset", transport.Adapt(h.PasswordReset))
	mux.HandleFunc("POST /api/v1/auth/password/change", transport.Adapt(h.AccessRequired(h.PasswordChange)))
}
