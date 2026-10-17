package authn

import (
	"strconv"
	"time"
)

// mailVars 组装邮件模板变量
// username 收件人用户名（用于问候语），token 一次性明文令牌
// 链接的域名与路径写死在邮件模板里（腾讯云 SES 审核要求固定域名），
// 后端只提供 token 变量，由模板渲染成 https://echat.qxbnx.cn/verify|reset?token=xxx
// 返回含 username / token / expire_minutes 的变量表，year 由 mailer 渲染时补齐
func mailVars(username, token string) map[string]string {
	return map[string]string{
		"username":       username,
		"token":          token,
		"expire_minutes": strconv.Itoa(int(tokenTTL / time.Minute)),
	}
}
