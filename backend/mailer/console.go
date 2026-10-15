package mailer

import (
	"context"

	"echat-backend/logging"
)

// ConsoleMailer 开发用邮件发送器，把邮件摘要打进日志，不做真实投递
type ConsoleMailer struct{}

// NewConsole 构造控制台邮件发送器
// 返回把邮件摘要打印到日志的 Mailer
func NewConsole() *ConsoleMailer {
	return &ConsoleMailer{}
}

// Send 打印一行邮件摘要
// ctx 链路上下文，未使用
// msg 邮件语义描述
// 始终返回 nil；用途未知时只告警不阻断
func (m *ConsoleMailer) Send(_ context.Context, msg Message) error {
	subject, err := subjectOf(msg.Kind)
	if err != nil {
		logging.L().Warnw("mailer·console 未知邮件用途", "kind", string(msg.Kind), "error", err)
		return nil
	}
	logging.L().Infow("mailer·console 发送邮件（开发用）",
		"to", msg.To,
		"kind", string(msg.Kind),
		"subject", subject,
		"action_url", msg.Vars["action_url"])
	return nil
}
