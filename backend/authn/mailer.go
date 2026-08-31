package authn

import (
	"context"
	"fmt"

	"echat-backend/logging"
)

// Mailer 发送验证/重置邮件的能力抽象
//
// 当前仅为控制台实现（打印日志），生产可替换为 SMTP / 第三方邮件服务，
// 接口隔离让发送方式与业务解耦
type Mailer interface {
	// Send 发送一封邮件到指定地址
	// ctx 链路上下文，to 收件人邮箱，subject 主题，body 正文
	Send(ctx context.Context, to, subject, body string) error
}

// ConsoleMailer 开发用邮件发送器：把邮件内容打进日志，便于本地联调
type ConsoleMailer struct{}

// Send 把邮件内容以 Infow 打到日志
func (ConsoleMailer) Send(ctx context.Context, to, subject, body string) error {
	logging.L().Infow("mailer·console 发送邮件（开发用）", "to", to, "subject", subject, "body", body)
	return nil
}

// verificationLink 拼接前端验证页链接
// baseURL 为前端验证页路径（如 http://localhost:5173/verify），token 为一次性明文令牌
func verificationLink(baseURL, token string) string {
	return fmt.Sprintf("%s?token=%s", baseURL, token)
}
