package mailer

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strconv"
	"time"
)

// 三套邮件 HTML 模板随二进制内嵌，无需运行时读盘

//go:embed templates/verify_email.html
var tplVerifyEmail string

//go:embed templates/bind_email.html
var tplBindEmail string

//go:embed templates/reset_password.html
var tplResetPassword string

// templateSpec 一套邮件模板的静态定义
type templateSpec struct {
	// subject 邮件主题
	subject string
	// body HTML 模板源码
	body string
}

// catalog 邮件用途到主题与模板的映射
var catalog = map[Kind]templateSpec{
	KindVerifyEmail:   {subject: "验证你的 eChat 邮箱", body: tplVerifyEmail},
	KindBindEmail:     {subject: "绑定你的 eChat 邮箱", body: tplBindEmail},
	KindResetPassword: {subject: "重置你的 eChat 密码", body: tplResetPassword},
}

// render 用变量渲染指定用途的邮件主题与 HTML 正文
// kind 邮件用途，决定模板
// vars 模板变量，缺省时自动补 year 为当前年份
// 返回主题与 HTML 正文；用途未知或模板解析失败时返回错误
func render(kind Kind, vars map[string]string) (subject, html string, err error) {
	spec, ok := catalog[kind]
	if !ok {
		return "", "", fmt.Errorf("未知邮件用途 %q", kind)
	}

	data := make(map[string]string, len(vars)+1)
	for k, v := range vars {
		data[k] = v
	}
	if data["year"] == "" {
		data["year"] = strconv.Itoa(time.Now().Year())
	}

	tpl, err := template.New(string(kind)).Parse(spec.body)
	if err != nil {
		return "", "", fmt.Errorf("解析 %s 模板: %w", kind, err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("渲染 %s 模板: %w", kind, err)
	}
	return spec.subject, buf.String(), nil
}
