package mailer

import (
	"context"

	"echat-backend/logging"
)

// ConsoleMailer 开发用邮件发送器，把邮件内容打进日志，便于本地联调
type ConsoleMailer struct{}

// NewConsole 构造控制台邮件发送器
// 返回把邮件内容打印到日志的 Mailer，不做真实投递
func NewConsole() *ConsoleMailer {
	return &ConsoleMailer{}
}

// Send 渲染邮件并把主题与关键变量打进日志
// ctx 链路上下文，未使用
// msg 邮件语义描述
// 始终返回 nil；用途未知时只告警不阻断
func (m *ConsoleMailer) Send(_ context.Context, msg Message) error {
	subject, html, err := render(msg.Kind, msg.Vars)
	if err != nil {
		logging.L().Warnw("mailer·console 渲染失败", "kind", string(msg.Kind), "error", err)
		return nil
	}
	logging.L().Infow("mailer·console 发送邮件（开发用）",
		"to", msg.To,
		"kind", string(msg.Kind),
		"subject", subject,
		"action_url", msg.Vars["action_url"],
		"html_bytes", len(html))
	return nil
}
