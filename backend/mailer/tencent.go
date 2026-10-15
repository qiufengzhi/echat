package mailer

import (
	"context"
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
// 正文走控制台模板：代码只提交模板变量，HTML 模板在腾讯云控制台维护
// 签名（TC3-HMAC-SHA256）与重试由官方 SDK 负责
type TencentMailer struct {
	// client SES API 客户端
	client *ses.Client
	// from FromEmailAddress，配了别名时形如 "别名 <地址>"
	from string
	// replyTo 回复地址，为空则不设置
	replyTo string
	// templateIDs 用途到控制台模板 ID 的映射，构造时已校验三种用途齐全
	templateIDs map[string]uint64
}

// NewTencent 按配置构造腾讯云 SES 发送器
// cfg 邮件配置，读取 from_address / from_alias / reply_to 与 tencent 子项
// 返回 SES Mailer；缺少密钥、发件人或任一用途的模板 ID 时返回错误
func NewTencent(cfg config.MailConfig) (*TencentMailer, error) {
	tc := cfg.Tencent
	if tc.SecretID == "" || tc.SecretKey == "" {
		return nil, fmt.Errorf("腾讯云邮件缺少 secret_id 或 secret_key")
	}
	if cfg.FromAddress == "" {
		return nil, fmt.Errorf("腾讯云邮件缺少 from_address")
	}
	for _, kind := range kinds {
		if tc.TemplateIDs[string(kind)] == 0 {
			return nil, fmt.Errorf("腾讯云邮件缺少用途 %s 的控制台模板 ID", kind)
		}
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
// 返回组装请求或接口调用失败的错误，成功为 nil
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
		"template_id", *req.Template.TemplateID,
		"message_id", deref(resp.Response.MessageId))
	return nil
}

// buildRequest 把邮件语义描述转换为 SES SendEmailRequest
// msg 邮件语义描述
// 返回填好的请求与邮件主题；用途未知、缺模板 ID 或变量序列化失败时返回错误
func (m *TencentMailer) buildRequest(msg Message) (*ses.SendEmailRequest, string, error) {
	subject, err := subjectOf(msg.Kind)
	if err != nil {
		return nil, "", err
	}
	templateID := m.templateIDs[string(msg.Kind)]
	if templateID == 0 {
		return nil, "", fmt.Errorf("邮件用途 %s 未配置控制台模板 ID", msg.Kind)
	}
	data, err := json.Marshal(withYear(msg.Vars))
	if err != nil {
		return nil, "", fmt.Errorf("序列化模板变量: %w", err)
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
	templateData := string(data)
	req.Template = &ses.Template{TemplateID: &templateID, TemplateData: &templateData}
	return req, subject, nil
}

// deref 安全解引用字符串指针，nil 返回空串
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
