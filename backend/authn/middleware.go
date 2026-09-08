// middleware.go access 校验：HTTP 中间件与 WebSocket 握手共用，并广播会话吊销给实时层
package authn

import (
	"context"
	"net/http"
	"time"

	"echat-backend/ent"
	"echat-backend/ent/session"
	"echat-backend/ent/user"
	"echat-backend/global"
	"echat-backend/transport"

	"github.com/google/uuid"
)

// wsAuthTimeout 鉴权上限（spec §9.4 鉴权超时）：防止数据库慢查询拖垮握手或请求
const wsAuthTimeout = 5 * time.Second

// ValidateAccess 校验 access token：签名+有效期 → 会话存活 → ver 与库值一致 → 账户正常
// 这是 HTTP 中间件与 WebSocket 握手共用的唯一入口，带超时防止慢查询阻塞
// ctx 链路上下文，accessToken 的 Authorization Bearer 或 WS ?token= 值
func (s *Service) ValidateAccess(ctx context.Context, accessToken string) (*AccessClaims, error) {
	if accessToken == "" {
		return nil, ErrAccessTokenInvalid
	}
	authCtx, cancel := context.WithTimeout(ctx, wsAuthTimeout)
	defer cancel()

	claims, err := s.parseAccessToken(accessToken)
	if err != nil {
		return nil, err
	}
	userID := claims.SubjectUUID()
	if userID == uuid.Nil || claims.SessionID == uuid.Nil {
		return nil, ErrAccessTokenInvalid
	}

	// 按 sid 取会话并带出用户：不存在 / 已吊销 / 已过期 / 版本不符均视为失效
	sess, err := s.client.Session.Query().
		Where(session.ID(claims.SessionID), session.UserID(userID)).
		WithUser().
		Only(authCtx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAccessTokenInvalid
		}
		return nil, ErrInternal
	}
	if sess.RevokedAt != nil || sess.ExpiresAt.Before(time.Now()) || sess.TokenVersion != claims.TokenVersion {
		return nil, ErrAccessTokenInvalid
	}

	u := sess.Edges.User
	switch u.Status {
	case user.StatusSuspended:
		return nil, ErrAccountSuspended
	case user.StatusActive:
	default: // pending / deleted 一律视为凭据无效
		return nil, ErrAccessTokenInvalid
	}
	// 用户级 token_version 被改密/重置/登出全部会话抬升后，旧 access 立即作废
	if u.TokenVersion != claims.TokenVersion {
		return nil, ErrAccessTokenInvalid
	}
	return claims, nil
}

// claimsKey context 中存放校验后 claims 的键类型，避免与其他键冲突
type claimsKey struct{}

// WithClaims 把校验后的 claims 注入 context
// ctx 原链路上下文，c 校验通过的身份声明
func WithClaims(ctx context.Context, c *AccessClaims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFrom 从 context 取 claims，未注入返回零值与 false
// 受保护端点用它取身份，签名与普通端点保持一致
func ClaimsFrom(ctx context.Context) (*AccessClaims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*AccessClaims)
	return c, ok
}

// AccessRequired 访问令牌中间件：校验通过后把 claims 注入 context
// 受保护端点用 ClaimsFrom 取身份，签名与普通端点一致
func (h *Handler) AccessRequired(next transport.HandlerFunc) transport.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		claims, err := h.svc.ValidateAccess(r.Context(), bearerToken(r))
		if err != nil {
			return toError(err)
		}
		return next(w, r.WithContext(WithClaims(r.Context(), claims)))
	}
}

// bearerToken 从 Authorization 头提取 Bearer access token，缺失返回空串
func bearerToken(r *http.Request) string {
	raw := r.Header.Get("Authorization")
	if len(raw) > 7 && raw[:7] == "Bearer " {
		return raw[7:]
	}
	return ""
}

// publishRevoked 进程内广播一次会话吊销，实时层（room）据此踢连接
// 用非阻塞发送：通道打满丢事件可接受（outbox 是持久真相，下游投影会兜底）
func (s *Service) publishRevoked(userID, keepSessionID uuid.UUID, reason string) {
	select {
	case global.UserRevokedCh <- global.UserRevokedEvent{UserID: userID, KeepSessionID: keepSessionID, Reason: reason}:
	default:
	}
}
