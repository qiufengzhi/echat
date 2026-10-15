// Package mailer 邮件发送抽象与各服务商实现
//
// 业务层只构造邮件意图（收件人 + 用途 + 模板变量），由 provider 负责渲染与投递
// 换服务商只改配置 mail.provider：console 打印日志，tencent 走腾讯云 SES
package mailer

import (
	"context"
	"fmt"

	"echat-backend/config"
)

// Kind 邮件用途，决定选用哪套主题与 HTML 模板
type Kind string

const (
	// KindVerifyEmail 注册后邮箱验证
	KindVerifyEmail Kind = "verify_email"
	// KindBindEmail 已登录用户绑定或换绑邮箱
	KindBindEmail Kind = "bind_email"
	// KindResetPassword 找回密码
	KindResetPassword Kind = "reset_password"
)

// Message 一封待发邮件的语义描述，与具体服务商解耦
type Message struct {
	// To 收件人邮箱地址
	To string
	// Kind 邮件用途，决定主题与模板
	Kind Kind
	// Vars 模板变量，键为模板占位符名，取值 username / action_url / expire_minutes
	Vars map[string]string
}

// Mailer 邮件发送能力抽象，各服务商各自实现
type Mailer interface {
	// Send 投递一封邮件
	// ctx 链路上下文
	// msg 邮件语义描述（收件人 + 用途 + 模板变量）
	// 返回发送失败的错误，成功为 nil
	Send(ctx context.Context, msg Message) error
}

// New 按 mail.provider 配置构造对应服务商的 Mailer
// cfg 邮件配置，provider 字段决定实现
// 返回选中的 Mailer；provider 取值未知或配置不完整时返回错误
func New(cfg config.MailConfig) (Mailer, error) {
	switch cfg.Provider {
	case "", "console":
		return NewConsole(), nil
	case "tencent":
		return NewTencent(cfg)
	default:
		return nil, fmt.Errorf("未知邮件服务商 %q，支持 console / tencent", cfg.Provider)
	}
}
