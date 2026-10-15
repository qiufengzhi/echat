package mailer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"echat-backend/config"
	"echat-backend/logging"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ses "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ses/v20201002"
)

// sesTriggerTransactional 触发类邮件，验证链接等即时邮件走此通道，取值 1
const sesTriggerTransactional = 1

// TencentMailer 腾讯云 SES（邮件推送）发送器
//
// 签名（TC3-HMAC-SHA256）与重试由官方 SDK 负责
type TencentMailer struct {
	// client SES API 客户端
	client *ses.Client
	// from FromEmailAddress，配了别名时形如 "别名 <地址>"
	from string
	// replyTo 回复地址，为空则不设置
	replyTo string
	// templateIDs 用途到控制台模板 ID 的映射，命中且非 0 时走模板发送，否则走内联 HTML
	templateIDs map[string]uint64
}

// NewTencent 按配置构造腾讯云 SES 发送器
// cfg 邮件配置，读取 from_address / from_alias / reply_to 与 tencent 子项
// 返回 SES Mailer；缺少密钥或 SDK 初始化失败时返回错误
func NewTencent(cfg config.MailConfig) (*TencentMailer, error) {
	tc := cfg.Tencent
	if tc.SecretID == "" || tc.SecretKey == "" {
		return nil, fmt.Errorf("腾讯云邮件缺少 secret_id 或 secret_key")
	}
	if cfg.FromAddress == "" {
		return nil, fmt.Errorf("腾讯云邮件缺少 from_address")
	}

	credential := common.NewCredential(tc.SecretID, tc.SecretKey)
	clientProfile := profile.NewClientProfile()
	if tc.Endpoint != "" {
		clientProfile.HttpProfile.Endpoint = tc.Endpoint
	}
	client, err := ses.NewClient(credential, tc.Region, clientProfile)
	if err != nil {
		return nil, fmt.Errorf("初始化腾讯云 SES 客户端: %w", err)
	}

	return &TencentMailer{
		client:      client,
		from:        fromAddress(cfg),
		replyTo:     cfg.ReplyTo,
		templateIDs: tc.TemplateIDs,
	}, nil
}

// fromAddress 拼装 FromEmailAddress，配了别名则形如 "别名 <地址>"
// cfg 邮件配置
// 返回可填入 SES FromEmailAddress 的字符串
func fromAddress(cfg config.MailConfig) string {
	if cfg.FromAlias == "" {
		return cfg.FromAddress
	}
	return fmt.Sprintf("%s <%s>", cfg.FromAlias, cfg.FromAddress)
}

// Send 通过腾讯云 SES 投递一封邮件
// ctx 链路上下文
// msg 邮件语义描述（收件人 + 用途 + 模板变量）
// 返回渲染或接口调用失败的错误，成功为 nil
func (m *TencentMailer) Send(ctx context.Context, msg Message) error {
	req, subject, err := m.buildRequest(msg)
	if err != nil {
		return err
	}

	resp, err := m.client.SendEmailWithContext(ctx, req)
	if err != nil {
		return fmt.Errorf("调用腾讯云 SES: %w", err)
	}
	logging.L().Infow("mailer·tencent 邮件已提交",
		"to", msg.To,
		"kind", string(msg.Kind),
		"subject", subject,
		"template", req.Template != nil,
		"message_id", deref(resp.Response.MessageId))
	return nil
}

// buildRequest 把邮件语义描述转换为 SES SendEmailRequest
// msg 邮件语义描述
// 返回填好的请求与渲染出的主题；模板渲染或变量序列化失败时返回错误
func (m *TencentMailer) buildRequest(msg Message) (*ses.SendEmailRequest, string, error) {
	subject, html, err := render(msg.Kind, msg.Vars)
	if err != nil {
		return nil, "", err
	}

	req := ses.NewSendEmailRequest()
	req.FromEmailAddress = &m.from
	req.Subject = &subject
	req.Destination = []*string{&msg.To}
	trigger := uint64(sesTriggerTransactional)
	req.TriggerType = &trigger
	if m.replyTo != "" {
		req.ReplyToAddresses = &m.replyTo
	}

	// 配了该用途的控制台模板 ID 则走模板发送，否则把内联 HTML 的 base64 装进 Simple
	if id := m.templateIDs[string(msg.Kind)]; id > 0 {
		data, err := json.Marshal(msg.Vars)
		if err != nil {
			return nil, "", fmt.Errorf("序列化模板变量: %w", err)
		}
		templateData := string(data)
		req.Template = &ses.Template{TemplateID: &id, TemplateData: &templateData}
	} else {
		encoded := base64.StdEncoding.EncodeToString([]byte(html))
		req.Simple = &ses.Simple{Html: &encoded}
	}
	return req, subject, nil
}

// deref 安全解引用字符串指针，nil 返回空串
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
