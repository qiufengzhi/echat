package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"echat-backend/config"
	"echat-backend/ent"
	"echat-backend/ent/authtoken"
	"echat-backend/ent/identity"
	"echat-backend/ent/user"

	"github.com/google/uuid"
)

// tokenTTL 一次性令牌有效期（默认 30 分钟）
const tokenTTL = 30 * time.Minute

// Service 认证域服务：编排注册、验证、绑定等用例
type Service struct {
	// client Ent ORM 客户端
	client *ent.Client
	// authCfg 认证配置（argon2 参数、JWT 密钥、各令牌有效期）
	authCfg config.AuthConfig
	// mailer 邮件发送抽象
	mailer Mailer
	// verifyBaseURL 前端验证页 URL 前缀，用于拼接一次性链接
	verifyBaseURL string
}

// NewService 构造认证服务
// client 持久层客户端，authCfg 认证配置，mailer 邮件发送器，verifyBaseURL 前端验证页前缀
func NewService(client *ent.Client, authCfg config.AuthConfig, mailer Mailer, verifyBaseURL string) *Service {
	return &Service{client: client, authCfg: authCfg, mailer: mailer, verifyBaseURL: verifyBaseURL}
}

// RegisterRequest 注册请求体
type RegisterRequest struct {
	// Username 用户名（3-20 位字母数字 _ -），本地账号登录标识
	Username string `json:"username"`
	// Password 登录密码，需通过强度校验（见 password.go）
	Password string `json:"password"`
	// Email 可选邮箱：空则本地账号注册直接生效（status=active）；非空则走邮箱验证（pending → active）
	Email string `json:"email"`
}

// RegisterResult 注册成功响应体
type RegisterResult struct {
	// ID 用户全局稳定身份
	ID uuid.UUID `json:"id"`
	// Username 用户名
	Username string `json:"username"`
	// Email 已绑定邮箱，未绑定为 null
	Email *string `json:"email"`
	// Status 注册后状态：active（本地账号）或 pending（邮箱待验证）
	Status string `json:"status"`
	// NeedVerify 是否需要邮箱验证（本地账号为 false）
	NeedVerify bool `json:"need_verify"`
}

// Register 注册用例：本地账号或邮箱账号，两种方式共用同一张 users 表
// ctx 链路上下文，req 注册请求
// 本地账号：users.status=active + identities(provider=local) + outbox user.registered
// 邮箱账号：users.status=pending + identities(provider=email) + 一次性验证令牌 + outbox user.registered
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error) {
	username := strings.TrimSpace(req.Username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	email := NormalizeEmail(req.Email)
	if email != "" {
		if err := ValidateEmail(email); err != nil {
			return nil, err
		}
	}
	if err := ValidatePassword(req.Password, []string{username, email}); err != nil {
		return nil, err
	}

	hash, err := HashPassword(s.authCfg.Argon2, req.Password)
	if err != nil {
		return nil, ErrInternal
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, ErrInternal
	}
	defer func() { _ = tx.Rollback() }()

	// 冲突预检（快速路径）：终判由数据库唯一索引兜底，并发下错误会被识别为约束冲突
	if exists, err := tx.User.Query().Where(user.UsernameEQ(username)).Exist(ctx); err != nil {
		return nil, ErrInternal
	} else if exists {
		return nil, ErrConflict
	}
	if email != "" {
		if exists, err := tx.Identity.Query().
			Where(identity.ProviderUIDEQ(email), identity.ProviderEQ(identity.ProviderEmail)).
			Exist(ctx); err != nil {
			return nil, ErrInternal
		} else if exists {
			return nil, ErrConflict
		}
	}

	userID := uuid.New()
	method := identity.ProviderLocal
	providerUID := username
	status := user.StatusActive
	if email != "" {
		method = identity.ProviderEmail
		providerUID = email
		status = user.StatusPending
	}

	created, err := tx.User.Create().
		SetID(userID).
		SetUsername(username).
		SetDisplayName(username).
		SetEmail(email).
		SetStatus(status).
		SetPasswordHash(hash).
		Save(ctx)
	if err != nil {
		return nil, constraintOrInternal(err)
	}

	if _, err := tx.Identity.Create().
		SetID(uuid.New()).
		SetUserID(created.ID).
		SetProvider(method).
		SetProviderUID(providerUID).
		Save(ctx); err != nil {
		return nil, constraintOrInternal(err)
	}

	var verifyToken string
	if email != "" {
		verifyToken, err = randomToken()
		if err != nil {
			return nil, ErrInternal
		}
		if _, err := tx.AuthToken.Create().
			SetID(uuid.New()).
			SetUserID(created.ID).
			SetPurpose(authtoken.PurposeVerifyEmail).
			SetTarget(email).
			SetTokenHash(hashToken(verifyToken)).
			SetExpiresAt(time.Now().Add(tokenTTL)).
			Save(ctx); err != nil {
			return nil, ErrInternal
		}
	}

	if err := recordEvent(ctx, tx, Event{
		Type:        EventTypeUserRegistered,
		AggregateID: created.ID,
		Subject:     userSubject(created.ID, "registered"),
		Payload:     map[string]any{"method": method},
	}); err != nil {
		return nil, ErrInternal
	}

	if err := tx.Commit(); err != nil {
		return nil, ErrInternal
	}

	// 邮件在事务提交后再发：发送失败不回滚账户，验证链接可由用户重新索取
	if verifyToken != "" {
		if err := s.mailer.Send(context.Background(), email, "验证你的 eChat 邮箱",
			"点击链接完成验证："+verificationLink(s.verifyBaseURL, verifyToken)); err != nil {
			return nil, ErrInternal
		}
	}

	return &RegisterResult{
		ID:         created.ID,
		Username:   username,
		Email:      optional(email),
		Status:     string(status),
		NeedVerify: email != "",
	}, nil
}

// VerifyEmail 校验一次性令牌：注册激活与绑定邮箱共用该端点
// ctx 链路上下文，token 明文一次性令牌
// pending 用户 → 激活并记 user.verified；active 用户 → 绑定邮箱并记 user.email.bound
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	if token == "" {
		return ErrInvalidToken
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ErrInternal
	}
	defer func() { _ = tx.Rollback() }()

	tok, err := tx.AuthToken.Query().
		Where(
			authtoken.TokenHashEQ(hashToken(token)),
			authtoken.PurposeEQ(authtoken.PurposeVerifyEmail),
			authtoken.ConsumedAtIsNil(),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrInvalidToken
		}
		return ErrInternal
	}
	if tok.ExpiresAt.Before(time.Now()) {
		return ErrInvalidToken
	}

	u, err := tok.QueryUser().Only(ctx)
	if err != nil {
		return ErrInternal
	}

	if _, err := tx.AuthToken.UpdateOneID(tok.ID).SetConsumedAt(time.Now()).Save(ctx); err != nil {
		return ErrInternal
	}

	if u.Status != user.StatusActive {
		if _, err := tx.User.UpdateOneID(u.ID).SetStatus(user.StatusActive).Save(ctx); err != nil {
			return ErrInternal
		}
		if err := recordEvent(ctx, tx, Event{
			Type:        EventTypeUserVerified,
			AggregateID: u.ID,
			Subject:     userSubject(u.ID, "verified"),
			Payload:     map[string]any{"email": tok.Target},
		}); err != nil {
			return ErrInternal
		}
	} else if tok.Target != "" {
		bindTarget := NormalizeEmail(tok.Target)
		if _, err := tx.Identity.Create().
			SetID(uuid.New()).
			SetUserID(u.ID).
			SetProvider(identity.ProviderEmail).
			SetProviderUID(bindTarget).
			Save(ctx); err != nil {
			return constraintOrInternal(err)
		}
		if _, err := tx.User.UpdateOneID(u.ID).SetEmail(bindTarget).Save(ctx); err != nil {
			return ErrInternal
		}
		if err := recordEvent(ctx, tx, Event{
			Type:        EventTypeUserEmailBound,
			AggregateID: u.ID,
			Subject:     userSubject(u.ID, "email.bound"),
			Payload:     map[string]any{"email": bindTarget},
		}); err != nil {
			return ErrInternal
		}
	}

	return tx.Commit()
}

// BindEmailRequest 绑定/换绑邮箱请求体
type BindEmailRequest struct {
	// Email 待绑定邮箱地址
	Email string `json:"email"`
}

// RequestEmailBind 为指定用户申请绑定/换绑邮箱：签发一次性令牌并发邮件
// ctx 链路上下文，userID 当前登录用户，req 目标邮箱
func (s *Service) RequestEmailBind(ctx context.Context, userID uuid.UUID, req BindEmailRequest) error {
	email := NormalizeEmail(req.Email)
	if err := ValidateEmail(email); err != nil {
		return err
	}

	// 邮箱已被他人绑定则提前拒绝（泛化报错），避免骚扰他人邮箱
	if exists, err := s.client.Identity.Query().
		Where(identity.ProviderUIDEQ(email), identity.ProviderEQ(identity.ProviderEmail)).
		Exist(ctx); err != nil {
		return ErrInternal
	} else if exists {
		return ErrConflict
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
		SetUserID(userID).
		SetPurpose(authtoken.PurposeVerifyEmail).
		SetTarget(email).
		SetTokenHash(hashToken(tok)).
		SetExpiresAt(time.Now().Add(tokenTTL)).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return constraintOrInternal(err)
	}
	if err := tx.Commit(); err != nil {
		return ErrInternal
	}

	if err := s.mailer.Send(context.Background(), email, "绑定你的 eChat 邮箱",
		"点击链接完成绑定："+verificationLink(s.verifyBaseURL, tok)); err != nil {
		return ErrInternal
	}
	return nil
}

// optional 空串转 nil 指针，用于响应里的可空邮箱字段
func optional(email string) *string {
	if email == "" {
		return nil
	}
	return &email
}

// randomToken 生成 32 字节安全随机的十六进制令牌
// 返回的 token 用于外发（邮件链接），库中只存它的 SHA-256 哈希
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hashToken 对明文令牌取 SHA-256 并十六进制编码，用于落库
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// constraintOrInternal 把数据库唯一约束冲突归一为泛化冲突，其余归为内部错误
func constraintOrInternal(err error) error {
	if ent.IsConstraintError(err) {
		return ErrConflict
	}
	return ErrInternal
}
