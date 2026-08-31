// login.go 登录与会话域：登录、刷新轮换、重放检测、退出、吊销
//
// 语义对齐 spec §4（登录与会话）与 §5（刷新、退出、吊销）
package authn

import (
	"context"
	"strings"
	"time"

	"echat-backend/ent"
	"echat-backend/ent/identity"
	"echat-backend/ent/session"
	"echat-backend/ent/user"

	"github.com/google/uuid"
)

// LoginRequest 登录请求体
type LoginRequest struct {
	// Identifier 登录标识：用户名 或 邮箱（含 @ 按邮箱解析）
	Identifier string `json:"identifier"`
	// Password 账户级密码（argon2id 校验，与本地/邮箱登录共用）
	Password string `json:"password"`
}

// SessionResult 登录/刷新成功响应体，见 spec §4.3 第 6 步
type SessionResult struct {
	// AccessToken 短命无状态 JWT
	AccessToken string `json:"access_token"`
	// TokenType 固定为 Bearer
	TokenType string `json:"token_type"`
	// ExpiresIn access 剩余秒数
	ExpiresIn int64 `json:"expires_in"`
	// RefreshToken 不透明刷新串，明文只此一次出现，之后走哈希比对
	RefreshToken string `json:"refresh_token"`
	// User 用户概要（含头像，登出页用）
	User UserInfo `json:"user"`
}

// UserInfo 认证响应中的用户概要
type UserInfo struct {
	// ID 用户全局稳定身份
	ID uuid.UUID `json:"id"`
	// Username 用户名句柄
	Username string `json:"username"`
	// DisplayName 展示昵称
	DisplayName string `json:"display_name"`
	// AvatarURL 头像地址，可空
	AvatarURL *string `json:"avatar_url"`
}

// Login 登录用例：解析标识 → 校验密码 → 建会话 → 双 token
// ctx 链路上下文，req 登录请求，ip 来源 IP（审计），ua 用户代理（审计）
// 返回会话凭据；失败锁定由 limiter 控制
func (s *Service) Login(ctx context.Context, req LoginRequest, ip, ua string) (*SessionResult, error) {
	identifier := strings.TrimSpace(req.Identifier)
	if identifier == "" || req.Password == "" {
		return nil, ErrBadRequest
	}

	// 命中既有登录标识的用户凭据（不含 password_hash，独立一步取）
	u, err := s.resolveUser(ctx, identifier)
	if err != nil {
		return nil, err
	}
	// 账户维度与 IP 维度双锁：任一锁定即拒绝（不泄露具体哪把）
	if s.limiter.Locked("login:"+u.ID.String()) || s.limiter.Locked("login:ip:"+ip) {
		return nil, ErrInvalidCredentials
	}

	cred, err := s.client.User.Query().
		Where(user.ID(u.ID)).
		Select(user.FieldPasswordHash, user.FieldStatus, user.FieldTokenVersion,
			user.FieldUsername, user.FieldDisplayName, user.FieldAvatarURL).
		Only(ctx)
	if err != nil {
		return nil, ErrInternal
	}

	match, err := VerifyPassword(cred.PasswordHash, req.Password)
	if err != nil || !match {
		s.limiter.Failed("login:" + u.ID.String())
		s.limiter.Failed("login:ip:" + ip)
		_ = s.recordLoginFailure(ctx, u.ID, ip)
		return nil, ErrInvalidCredentials
	}
	s.limiter.Reset("login:" + u.ID.String())
	s.limiter.Reset("login:ip:" + ip)

	switch cred.Status {
	case user.StatusActive:
	case user.StatusSuspended:
		return nil, ErrAccountSuspended
	default: // pending / deleted 等一律视为凭据无效
		return nil, ErrInvalidCredentials
	}

	return s.openSession(ctx, cred, ip, ua, "")
}

// openSession 为已通过校验的用户创建会话并签发双 token
// cred 必须是已 Select 密码外全部所需字段的用户实体
func (s *Service) openSession(ctx context.Context, cred *ent.User, ip, ua, fp string) (*SessionResult, error) {
	refreshPlain, err := newRefreshToken()
	if err != nil {
		return nil, ErrInternal
	}
	sessionID := uuid.New()
	expiresAt := time.Now().Add(s.refreshTTL())

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, ErrInternal
	}
	if _, err := tx.Session.Create().
		SetID(sessionID).
		SetUserID(cred.ID).
		SetRefreshTokenHash(hashToken(refreshPlain)).
		SetTokenVersion(cred.TokenVersion).
		SetExpiresAt(expiresAt).
		SetIP(ip).
		SetUserAgent(ua).
		SetDeviceFingerprint(fp).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}

	id := cred.ID
	if err := recordEvent(ctx, tx.OutboxEvent, Event{
		Type:        EventTypeUserLoginSucceeded,
		AggregateID: id,
		Subject:     userSubject(id, "login.succeeded"),
		Payload:     map[string]any{"ip": ip, "ua": ua, "device_fingerprint": fp},
	}); err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrInternal
	}

	access, _, err := s.signAccessToken(cred.ID, sessionID, cred.TokenVersion)
	if err != nil {
		return nil, ErrInternal
	}
	return s.sessionResult(access, refreshPlain, cred), nil
}

// Refresh 刷新用例：轮换 refresh，旧令牌作废；对已作废令牌视为重放并清算
// ctx 链路上下文，refreshToken 明文刷新串，ip 审计
func (s *Service) Refresh(ctx context.Context, refreshToken, ip string) (*SessionResult, error) {
	if refreshToken == "" {
		return nil, ErrBadRequest
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, ErrInternal
	}
	sess, err := tx.Session.Query().
		Where(session.RefreshTokenHashEQ(hashToken(refreshToken))).
		Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsNotFound(err) {
			return nil, ErrInvalidToken
		}
		return nil, ErrInternal
	}

	// 重放检测：已作废的 refresh 被再次使用 → 判定令牌泄露 → 清算全部会话
	if sess.RevokedAt != nil {
		_ = s.revokeAllAndBump(ctx, sess.UserID)
		_ = tx.Rollback()
		return nil, ErrInvalidToken
	}
	if sess.ExpiresAt.Before(time.Now()) {
		_ = tx.Rollback()
		return nil, ErrInvalidToken
	}

	u, err := tx.User.Query().Where(user.ID(sess.UserID)).Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}
	if u.Status != user.StatusActive {
		_ = tx.Rollback()
		if u.Status == user.StatusSuspended {
			return nil, ErrAccountSuspended
		}
		return nil, ErrInvalidToken
	}
	if sess.TokenVersion != u.TokenVersion {
		_ = tx.Rollback()
		return nil, ErrInvalidToken
	}

	// 轮换：新 refresh 换新会话记录，旧 refresh 作废（保留哈希供重放检测）
	newPlain, err := newRefreshToken()
	if err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}
	newID := uuid.New()
	now := time.Now()
	if _, err := tx.Session.UpdateOneID(sess.ID).SetRevokedAt(now).Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}
	if _, err := tx.Session.Create().
		SetID(newID).
		SetUserID(u.ID).
		SetRefreshTokenHash(hashToken(newPlain)).
		SetTokenVersion(u.TokenVersion).
		SetExpiresAt(now.Add(s.refreshTTL())).
		SetIP(ip).
		SetUserAgent(sess.UserAgent).
		SetDeviceFingerprint(sess.DeviceFingerprint).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}

	// 记录刷新审计事件（复用登录成功事件语意，subject 区分会话）
	if err := recordEvent(ctx, tx.OutboxEvent, Event{
		Type:        EventTypeUserLoginSucceeded,
		AggregateID: u.ID,
		Subject:     userSubject(u.ID, "refresh"),
		Payload:     map[string]any{"ip": ip, "rotated_from": sess.ID.String()},
	}); err != nil {
		_ = tx.Rollback()
		return nil, ErrInternal
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrInternal
	}

	access, _, err := s.signAccessToken(u.ID, newID, u.TokenVersion)
	if err != nil {
		return nil, ErrInternal
	}
	return &SessionResult{
		AccessToken:  access,
		TokenType:    "Bearer",
		RefreshToken: newPlain,
		User: UserInfo{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			AvatarURL:   u.AvatarURL,
		},
	}, nil
}

// Logout 单设备退出：吊销当前 refresh 会话（不清 token_version）
// ctx 链路上下文，refreshToken 明文刷新串
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return ErrBadRequest
	}
	_, err := s.client.Session.Update().
		Where(
			session.RefreshTokenHashEQ(hashToken(refreshToken)),
			session.RevokedAtIsNil(),
		).
		SetRevokedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return ErrInternal
	}
	return nil
}

// LogoutAll 全部设备退出：吊销该用户全部会话并 +1 token_version（旧 access 全废）
// ctx 链路上下文，userID 目标用户
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	if err := s.revokeAllAndBump(ctx, userID); err != nil {
		return err
	}
	// 广播吊销给实时层，该用户的全部 WS 连接立即被踢
	s.publishRevoked(userID, uuid.Nil, "logout-all")
	return nil
}

// revokeAllAndBump 吊销用户全部会话并把 token_version+1（置旧 access 立即失效）
func (s *Service) revokeAllAndBump(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ErrInternal
	}
	now := time.Now()
	if _, err := tx.Session.Update().
		Where(session.UserID(userID), session.RevokedAtIsNil()).
		SetRevokedAt(now).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if _, err := tx.User.UpdateOneID(userID).AddTokenVersion(1).Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	return tx.Commit()
}

// sessionResult 组装登录响应体
func (s *Service) sessionResult(access, refresh string, cred *ent.User) *SessionResult {
	ttl, _ := time.ParseDuration(s.authCfg.AccessTokenTTL)
	return &SessionResult{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(ttl.Seconds()),
		RefreshToken: refresh,
		User: UserInfo{
			ID:          cred.ID,
			Username:    cred.Username,
			DisplayName: cred.DisplayName,
			AvatarURL:   cred.AvatarURL,
		},
	}
}

// refreshTTL 解析 refresh 有效期，非法兜底 30 天
func (s *Service) refreshTTL() time.Duration {
	ttl, err := time.ParseDuration(s.authCfg.RefreshTokenTTL)
	if err != nil || ttl <= 0 {
		return 30 * 24 * time.Hour
	}
	return ttl
}

// resolveUser 按登录标识定位用户：含 @ 走邮箱 identity，否则按用户名
func (s *Service) resolveUser(ctx context.Context, identifier string) (*ent.User, error) {
	var u *ent.User
	var err error
	if strings.Contains(identifier, "@") {
		u, err = s.client.Identity.Query().
			Where(identity.ProviderEQ(identity.ProviderEmail), identity.ProviderUIDEQ(NormalizeEmail(identifier))).
			QueryUser().
			Only(ctx)
	} else {
		u, err = s.client.User.Query().Where(user.UsernameEQ(identifier)).Only(ctx)
	}
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrInvalidCredentials
		}
		return nil, ErrInternal
	}
	return u, nil
}

// recordLoginFailure 记录登录失败审计事件（含 IP），失败不入交易（防重放锁干扰）
func (s *Service) recordLoginFailure(ctx context.Context, userID uuid.UUID, ip string) error {
	return recordEvent(ctx, s.client.OutboxEvent, Event{
		Type:        EventTypeUserLoginFailed,
		AggregateID: userID,
		Subject:     userSubject(userID, "login.failed"),
		Payload:     map[string]any{"ip": ip},
	})
}
