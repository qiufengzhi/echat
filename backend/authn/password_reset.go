// password_reset.go 找回密码 / 修改密码，语义对齐 spec §6
//
// 找回与验证共用 auth_tokens 表（purpose=reset_password），只存哈希、30 分钟过期、用后即消费
package authn

import (
	"context"
	"time"

	"echat-backend/ent"
	"echat-backend/ent/authtoken"
	"echat-backend/ent/identity"
	"echat-backend/ent/session"
	"echat-backend/ent/user"

	"github.com/google/uuid"
)

// PasswordResetRequest 找回密码申请请求体
type PasswordResetRequest struct {
	// Email 已绑定邮箱（本地账号需先绑定才能在改密前找回）
	Email string `json:"email"`
}

// RequestPasswordReset 申请找回密码：签发一次性重置令牌并发邮件
// 无论邮箱是否存在都返回同一结果（防枚举），不存在的邮箱不真的发信
func (s *Service) RequestPasswordReset(ctx context.Context, req PasswordResetRequest) error {
	email := NormalizeEmail(req.Email)
	if err := ValidateEmail(email); err != nil {
		return err
	}

	u, err := s.client.Identity.Query().
		Where(identity.ProviderEQ(identity.ProviderEmail), identity.ProviderUIDEQ(email)).
		QueryUser().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil // 邮箱不存在：假装成功、不落地令牌、不发送，防账户探测
	}
	if err != nil {
		return ErrInternal
	}

	tok, err := randomToken()
	if err != nil {
		return ErrInternal
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ErrInternal
	}
	if _, err := tx.AuthToken.Create().
		SetID(uuid.New()).
		SetUserID(u.ID).
		SetPurpose(authtoken.PurposeResetPassword).
		SetTarget(email).
		SetTokenHash(hashToken(tok)).
		SetExpiresAt(time.Now().Add(tokenTTL)).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if err := tx.Commit(); err != nil {
		return ErrInternal
	}

	if err := s.mailer.Send(context.Background(), email, "重置你的 eChat 密码",
		"点击链接重置密码（30 分钟内有效）："+verificationLink(s.authCfg.ResetBaseURL, tok)); err != nil {
		return ErrInternal
	}
	return nil
}

// ResetPassword 用一次性令牌重置密码：新哈希 + version+1 + 吊销全会话
// ctx 链路上下文，token 一次性重置令牌，newPassword 新密码
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if token == "" {
		return ErrInvalidToken
	}
	if err := ValidatePassword(newPassword, nil); err != nil {
		return err
	}
	hash, err := HashPassword(s.authCfg.Argon2, newPassword)
	if err != nil {
		return ErrInternal
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ErrInternal
	}
	tok, err := tx.AuthToken.Query().
		Where(
			authtoken.TokenHashEQ(hashToken(token)),
			authtoken.PurposeEQ(authtoken.PurposeResetPassword),
			authtoken.ConsumedAtIsNil(),
		).
		Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsNotFound(err) {
			return ErrInvalidToken
		}
		return ErrInternal
	}
	if tok.ExpiresAt.Before(time.Now()) {
		_ = tx.Rollback()
		return ErrInvalidToken
	}

	if _, err := tx.AuthToken.UpdateOneID(tok.ID).SetConsumedAt(time.Now()).Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if _, err := tx.User.UpdateOneID(tok.UserID).SetPasswordHash(hash).AddTokenVersion(1).Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	// 重置走「借据越界」语义：一律吊销全部会话
	if _, err := tx.Session.Update().
		Where(session.UserID(tok.UserID), session.RevokedAtIsNil()).
		SetRevokedAt(time.Now()).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if err := recordEvent(ctx, tx.OutboxEvent, Event{
		Type:        EventTypeUserPasswordChanged,
		AggregateID: tok.UserID,
		Subject:     userSubject(tok.UserID, "password.changed"),
		Payload:     map[string]any{"method": "reset"},
	}); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if err := tx.Commit(); err != nil {
		return ErrInternal
	}
	// 重置改了密码并吊销全部会话，实时连接全部踢下线
	s.publishRevoked(tok.UserID, uuid.Nil, "password.reset")
	return nil
}

// ChangePasswordRequest 修改密码请求体（已登录）
type ChangePasswordRequest struct {
	// CurrentPassword 当前密码，用于校验所有者
	CurrentPassword string `json:"currentPassword"`
	// NewPassword 新密码，需通过强度校验
	NewPassword string `json:"newPassword"`
}

// ChangePassword 已登录改密：校验旧密码 → 新哈希 → version+1 → 吊销本设备之外会话
// ctx 链路上下文，userID 当前用户，sessionID 当前会话，req 新旧密码
func (s *Service) ChangePassword(ctx context.Context, userID, sessionID uuid.UUID, req ChangePasswordRequest) error {
	if req.NewPassword == "" {
		return ErrBadRequest
	}
	if err := ValidatePassword(req.NewPassword, nil); err != nil {
		return err
	}

	u, err := s.client.User.Query().
		Where(user.ID(userID)).
		Select(user.FieldPasswordHash, user.FieldTokenVersion).
		Only(ctx)
	if err != nil {
		return ErrInternal
	}
	match, err := VerifyPassword(u.PasswordHash, req.CurrentPassword)
	if err != nil || !match {
		return ErrInvalidCredentials
	}

	hash, err := HashPassword(s.authCfg.Argon2, req.NewPassword)
	if err != nil {
		return ErrInternal
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ErrInternal
	}
	now := time.Now()
	updated, err := tx.User.UpdateOneID(userID).
		SetPasswordHash(hash).
		AddTokenVersion(1).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	// 吊销本设备之外的全部会话，保留当前会话但把其 token_version 对齐，
	// 使该会话的 refresh 仍能用（access 靠旧 ver 被中间件拒绝 → 触发刷新换新）
	if _, err := tx.Session.Update().
		Where(session.UserID(userID), session.RevokedAtIsNil(), session.IDNEQ(sessionID)).
		SetRevokedAt(now).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if _, err := tx.Session.Update().
		Where(session.ID(sessionID), session.UserID(userID)).
		SetTokenVersion(updated.TokenVersion).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if err := recordEvent(ctx, tx.OutboxEvent, Event{
		Type:        EventTypeUserPasswordChanged,
		AggregateID: userID,
		Subject:     userSubject(userID, "password.changed"),
		Payload:     map[string]any{"method": "change"},
	}); err != nil {
		_ = tx.Rollback()
		return ErrInternal
	}
	if err := tx.Commit(); err != nil {
		return ErrInternal
	}
	// 改密只保留当前会话，其余会话吊销 → 踢掉这些会话的实时连接（保留当前连接）
	s.publishRevoked(userID, sessionID, "password.change")
	return nil
}
