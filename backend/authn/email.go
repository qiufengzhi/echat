package authn

import (
	"fmt"
	"strconv"
	"time"
)

// verificationLink 拼接前端验证页链接
// baseURL 为前端验证页路径（如 http://localhost:5173/verify），token 为一次性明文令牌
func verificationLink(baseURL, token string) string {
	return fmt.Sprintf("%s?token=%s", baseURL, token)
}

// mailVars 组装邮件模板变量
// username 收件人用户名（用于问候语），actionURL 邮件中的一次性操作链接
// 返回含 username / action_url / expire_minutes 的变量表，year 由 mailer 渲染时补齐
func mailVars(username, actionURL string) map[string]string {
	return map[string]string{
		"username":       username,
		"action_url":     actionURL,
		"expire_minutes": strconv.Itoa(int(tokenTTL / time.Minute)),
	}
}
