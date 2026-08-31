// tokens.go 双 token 方案：无状态 access(JWT) + 可吊销 refresh(不透明串)
//
// 只存 refresh 的哈希；access 用 HS256 签发，语义见 spec §4.2
package authn

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// jwtIssuer 签名声明里的签发者
const jwtIssuer = "echat-backend"

// AccessClaims access token（JWT HS256）的载荷声明，见 spec §4.2
type AccessClaims struct {
	// SessionID 会话 id，与 refresh 对应的一条 session 记录绑定
	SessionID uuid.UUID `json:"sid"`
	// TokenVersion 签发时的用户 token_version，中间件比对库值，不一致即失效
	TokenVersion int `json:"ver"`
	// RegisteredClaims 标准声明（sub=user_id, iss, iat, exp）
	jwt.RegisteredClaims
}

// signAccessToken 签发无状态 access token，返回明文与有效期
// userID 归属用户，sessionID 会话 id，version 当前 token_version
func (s *Service) signAccessToken(userID, sessionID uuid.UUID, version int) (string, time.Duration, error) {
	ttl, err := time.ParseDuration(s.authCfg.AccessTokenTTL)
	if err != nil || ttl <= 0 {
		ttl = 15 * time.Minute // 配置非法时兜底 15 分钟
	}
	now := time.Now()
	claims := AccessClaims{
		SessionID:    sessionID,
		TokenVersion: version,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.authCfg.JWTSecret))
	if err != nil {
		return "", 0, err
	}
	return signed, ttl, nil
}

// parseAccessToken 校验签名与有效期并解析 access token，供鉴权中间件调用
// tokenStr Authorization 头里的 JWT，成功返回载荷声明
func (s *Service) parseAccessToken(tokenStr string) (*AccessClaims, error) {
	tok, err := jwt.ParseWithClaims(tokenStr, &AccessClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.authCfg.JWTSecret), nil
	})
	if err != nil || !tok.Valid {
		return nil, ErrAccessTokenInvalid
	}
	claims, ok := tok.Claims.(*AccessClaims)
	if !ok {
		return nil, ErrAccessTokenInvalid
	}
	return claims, nil
}

// SubjectUUID 解析 sub 声明为 user_id；解析失败返回零值（调用方需自行兜底）
func (c *AccessClaims) SubjectUUID() uuid.UUID {
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// newRefreshToken 生成 refresh 明文并返回（落库用哈希，明文只回给客户端一次）
func newRefreshToken() (plain string, err error) {
	return randomToken()
}
