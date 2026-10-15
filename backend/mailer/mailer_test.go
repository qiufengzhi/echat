package mailer

import (
	"context"
	"strings"
	"testing"

	"echat-backend/config"
)

// testVars 构造一份完整的模板变量
func testVars() map[string]string {
	return map[string]string{
		"username":       "bob",
		"action_url":     "http://x/verify?token=t",
		"expire_minutes": "30",
	}
}

// testTemplateIDs 构造三种用途齐全的模板 ID 映射
func testTemplateIDs() map[string]uint64 {
	return map[string]uint64{
		string(KindVerifyEmail):   11,
		string(KindBindEmail):     22,
		string(KindResetPassword): 33,
	}
}

// TestSubjectOf 校验已知用途有主题、未知用途报错
func TestSubjectOf(t *testing.T) {
	for _, kind := range kinds {
		if subject, err := subjectOf(kind); err != nil || subject == "" {
			t.Errorf("subjectOf(%s) 应有非空主题，得到 %q err=%v", kind, subject, err)
		}
	}
	if _, err := subjectOf(Kind("nope")); err == nil {
		t.Fatal("未知用途应返回错误")
	}
}

// TestWithYear 校验缺省补年份、已有年份不被覆盖、且不修改入参
func TestWithYear(t *testing.T) {
	vars := map[string]string{"username": "bob"}
	got := withYear(vars)
	if got["year"] == "" {
		t.Error("应补齐 year")
	}
	if _, ok := vars["year"]; ok {
		t.Error("不应修改入参")
	}
	if got := withYear(map[string]string{"year": "1999"}); got["year"] != "1999" {
		t.Errorf("已有年份不应被覆盖，得到 %q", got["year"])
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

// TestNewTencentRequiresConfig 校验腾讯云 provider 的必填项逐项校验
func TestNewTencentRequiresConfig(t *testing.T) {
	full := config.MailConfig{
		Provider:    "tencent",
		FromAddress: "noreply@mail.example.com",
		Tencent:     config.TencentMailConf{SecretID: "id", SecretKey: "key", Region: "ap-guangzhou", TemplateIDs: testTemplateIDs()},
	}
	if _, err := New(full); err != nil {
		t.Fatalf("配置齐全应构造成功: %v", err)
	}

	cases := map[string]config.MailConfig{
		"缺密钥":    {Provider: "tencent", FromAddress: "a@b.c", Tencent: config.TencentMailConf{TemplateIDs: testTemplateIDs()}},
		"缺发件人":   {Provider: "tencent", Tencent: config.TencentMailConf{SecretID: "id", SecretKey: "key", TemplateIDs: testTemplateIDs()}},
		"缺模板 ID": {Provider: "tencent", FromAddress: "a@b.c", Tencent: config.TencentMailConf{SecretID: "id", SecretKey: "key"}},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s 应返回错误", name)
		}
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

// TestTencentBuildRequest 校验请求走控制台模板并携带变量
func TestTencentBuildRequest(t *testing.T) {
	m := &TencentMailer{from: "eChat <noreply@mail.example.com>", templateIDs: testTemplateIDs()}
	req, subject, err := m.buildRequest(Message{To: "bob@example.com", Kind: KindVerifyEmail, Vars: testVars()})
	if err != nil {
		t.Fatalf("buildRequest 出错: %v", err)
	}
	if subject == "" {
		t.Error("主题为空")
	}
	if req.Template == nil {
		t.Fatal("应走控制台模板发送")
	}
	if *req.Template.TemplateID != 11 {
		t.Errorf("模板 ID 不符: %d", *req.Template.TemplateID)
	}
	if !strings.Contains(*req.Template.TemplateData, "bob") {
		t.Error("模板变量未包含 username")
	}
	if !strings.Contains(*req.Template.TemplateData, "year") {
		t.Error("模板变量未补齐 year")
	}
	if got := *req.Destination[0]; got != "bob@example.com" {
		t.Errorf("收件人不符: %s", got)
	}
	if got := *req.FromEmailAddress; got != "eChat <noreply@mail.example.com>" {
		t.Errorf("发件人不符: %s", got)
	}
}

// TestTencentBuildRequestMissingTemplate 校验缺模板 ID 时组装请求报错
func TestTencentBuildRequestMissingTemplate(t *testing.T) {
	m := &TencentMailer{from: "a@b.c", templateIDs: map[string]uint64{}}
	if _, _, err := m.buildRequest(Message{To: "bob@example.com", Kind: KindVerifyEmail, Vars: testVars()}); err == nil {
		t.Fatal("缺模板 ID 应返回错误")
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
