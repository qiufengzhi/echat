package authn

import (
	"regexp"
	"strings"

	"echat-backend/config"

	"github.com/alexedwards/argon2id"
	"github.com/nbutton23/zxcvbn-go"
)

// minPasswordLen 密码长度下限（OWASP：不强制字符组合，只求够长）
const minPasswordLen = 8

// weakPasswordScore 拒绝低于该分数(0-4)的密码：0-1 视为过于简单
const weakPasswordScore = 2

// usernameRe 用户名规则：3-20 位字母数字 _ -
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,20}$`)

// emailRe 邮箱近似校验（RFC 完整校验留待发送前的真实投递校验）
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// HashPassword 用 argon2id 哈希密码，参数取自 auth.argon2 配置（OWASP 推荐记忆密集参数）
// plain 明文密码，encoded 为自描述哈希串，格式 $argon2id$v=19$m=...,t=...,p=...$salt$hash
func HashPassword(cfg config.Argon2Config, plain string) (encoded string, err error) {
	params := &argon2id.Params{
		Memory:      cfg.MemoryKiB,
		Iterations:  cfg.Iterations,
		Parallelism: cfg.Parallelism,
		SaltLength:  cfg.SaltLength,
		KeyLength:   cfg.KeyLength,
	}
	return argon2id.CreateHash(plain, params)
}

// VerifyPassword 校验明文密码是否匹配存储的 argon2id 哈希
// plain 明文密码，encoded 存储的自描述哈希串，返回是否匹配
func VerifyPassword(encoded, plain string) (match bool, err error) {
	return argon2id.ComparePasswordAndHash(plain, encoded)
}

// NormalizeEmail 规范化邮箱：去首尾空白并转小写，用于存储与唯一约束
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateUsername 校验用户名格式（3-20 位字母数字 _ -）
func ValidateUsername(username string) *Error {
	if !usernameRe.MatchString(username) {
		return ErrBadRequest
	}
	return nil
}

// ValidateEmail 校验邮箱格式（近似校验）
func ValidateEmail(email string) *Error {
	if !emailRe.MatchString(email) {
		return ErrBadRequest
	}
	return nil
}

// ValidatePassword 校验密码强度：长度下限 + zxcvbn 熵评分
// inputs 是评分上下文（用户名、邮箱），让「用户名做密码」这类情形被扣分
func ValidatePassword(password string, inputs []string) *Error {
	if len(password) < minPasswordLen {
		return ErrWeakPassword
	}
	if zxcvbn.PasswordStrength(password, inputs).Score < weakPasswordScore {
		return ErrWeakPassword
	}
	return nil
}
