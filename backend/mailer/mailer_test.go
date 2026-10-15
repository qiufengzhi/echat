package mailer

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"echat-backend/config"
)

// allKinds 覆盖全部邮件用途，供表驱动用例遍历
var allKinds = []Kind{KindVerifyEmail, KindBindEmail, KindResetPassword}

// TestRenderReplacesAllPlaceholders 校验三套模板都能把变量全部替换且不残留占位符
func TestRenderReplacesAllPlaceholders(t *testing.T) {
	vars := map[string]string{
		"username":       "alice",
		"action_url":     "http://localhost:5173/verify?token=abc",
		"expire_minutes": "30",
	}
	for _, kind := range allKinds {
		subject, html, err := render(kind, vars)
		if err != nil {
			t.Fatalf("render(%s) 出错: %v", kind, err)
		}
		if subject == "" {
			t.Errorf("render(%s) 主题为空", kind)
		}
		if strings.Contains(html, "{{") {
			t.Errorf("render(%s) 残留未替换占位符", kind)
		}
		if !strings.Contains(html, "alice") {
			t.Errorf("render(%s) 未注入 username", kind)
		}
		if !strings.Contains(html, "token=abc") {
			t.Errorf("render(%s) 未注入 action_url", kind)
		}
	}
}

// TestRenderInjectsYear 校验未显式传 year 时自动补齐当前年份
func TestRenderInjectsYear(t *testing.T) {
	_, html, err := render(KindVerifyEmail, map[string]string{})
	if err != nil {
		t.Fatalf("render 出错: %v", err)
	}
	if strings.Contains(html, "{{") {
		t.Error("year 未自动补齐")
	}
}

// TestRenderUnknownKind 校验未知用途返回错误
func TestRenderUnknownKind(t *testing.T) {
	if _, _, err := render(Kind("nope"), nil); err == nil {
		t.Fatal("未知用途应返回错误")
	}
}

// TestNewSelectsProvider 校验工厂按 provider 选择实现并拒绝未知值
func TestNewSelectsProvider(t *testing.T) {
	for _, provider := range []string{"", "console"} {
		m, err := New(config.MailConfig{Provider: provider})
		if err != nil {
			t.Fatalf("provider=%q 应回落 console: %v", provider, err)
		}
		if _, ok := m.(*ConsoleMailer); !ok {
			t.Fatalf("provider=%q 期望 *ConsoleMailer，得到 %T", provider, m)
		}
	}
	if _, err := New(config.MailConfig{Provider: "nope"}); err == nil {
		t.Fatal("未知 provider 应返回错误")
	}
}

// TestNewTencentRequiresConfig 校验腾讯云 provider 的必填项校验
func TestNewTencentRequiresConfig(t *testing.T) {
	if _, err := New(config.MailConfig{Provider: "tencent"}); err == nil {
		t.Fatal("缺密钥与发件人应返回错误")
	}
	if _, err := New(config.MailConfig{
		Provider: "tencent",
		Tencent:  config.TencentMailConf{SecretID: "id", SecretKey: "key", Region: "ap-guangzhou"},
	}); err == nil {
		t.Fatal("缺发件人应返回错误")
	}
	if _, err := New(config.MailConfig{
		Provider:    "tencent",
		FromAddress: "noreply@mail.example.com",
		Tencent:     config.TencentMailConf{SecretID: "id", SecretKey: "key", Region: "ap-guangzhou"},
	}); err != nil {
		t.Fatalf("配置齐全应构造成功: %v", err)
	}
}

// TestFromAddress 校验发件人字段在有/无别名时的拼装
func TestFromAddress(t *testing.T) {
	if got := fromAddress(config.MailConfig{FromAddress: "a@b.c"}); got != "a@b.c" {
		t.Errorf("无别名应只返回地址，得到 %q", got)
	}
	if got := fromAddress(config.MailConfig{FromAddress: "a@b.c", FromAlias: "eChat"}); got != "eChat <a@b.c>" {
		t.Errorf("有别名应拼成 别名 <地址>，得到 %q", got)
	}
}

// testVars 构造一份完整的模板变量
func testVars() map[string]string {
	return map[string]string{
		"username":       "bob",
		"action_url":     "http://x/verify?token=t",
		"expire_minutes": "30",
	}
}

// TestTencentBuildRequestInlineHTML 校验未配模板 ID 时走内联 HTML 且 base64 编码
func TestTencentBuildRequestInlineHTML(t *testing.T) {
	m := &TencentMailer{from: "eChat <noreply@mail.example.com>"}
	req, subject, err := m.buildRequest(Message{To: "bob@example.com", Kind: KindVerifyEmail, Vars: testVars()})
	if err != nil {
		t.Fatalf("buildRequest 出错: %v", err)
	}
	if subject == "" {
		t.Error("主题为空")
	}
	if req.Template != nil {
		t.Error("未配模板 ID 时不应走模板发送")
	}
	if req.Simple == nil || req.Simple.Html == nil {
		t.Fatal("应走内联 HTML")
	}
	decoded, err := base64.StdEncoding.DecodeString(*req.Simple.Html)
	if err != nil {
		t.Fatalf("Simple.Html 应为 base64: %v", err)
	}
	if !strings.Contains(string(decoded), "token=t") {
		t.Error("内联 HTML 未包含 action_url")
	}
	if got := *req.Destination[0]; got != "bob@example.com" {
		t.Errorf("收件人不符: %s", got)
	}
}

// TestTencentBuildRequestTemplate 校验配了模板 ID 时走控制台模板并携带变量
func TestTencentBuildRequestTemplate(t *testing.T) {
	m := &TencentMailer{from: "eChat <noreply@mail.example.com>", templateIDs: map[string]uint64{string(KindVerifyEmail): 42}}
	req, _, err := m.buildRequest(Message{To: "bob@example.com", Kind: KindVerifyEmail, Vars: testVars()})
	if err != nil {
		t.Fatalf("buildRequest 出错: %v", err)
	}
	if req.Template == nil {
		t.Fatal("配了模板 ID 应走模板发送")
	}
	if *req.Template.TemplateID != 42 {
		t.Errorf("模板 ID 不符: %d", *req.Template.TemplateID)
	}
	if req.Simple != nil {
		t.Error("模板发送不应设置 Simple")
	}
	if !strings.Contains(*req.Template.TemplateData, "bob") {
		t.Error("模板变量未包含 username")
	}
}

// TestConsoleSendNeverFails 校验控制台发送不因用途未知而阻断调用方
func TestConsoleSendNeverFails(t *testing.T) {
	m := NewConsole()
	if err := m.Send(context.Background(), Message{To: "x@y.z", Kind: KindVerifyEmail, Vars: testVars()}); err != nil {
		t.Fatalf("console 发送不应报错: %v", err)
	}
	if err := m.Send(context.Background(), Message{To: "x@y.z", Kind: Kind("nope")}); err != nil {
		t.Fatalf("未知用途也只告警不报错: %v", err)
	}
}
