package mailer

import (
	"fmt"
	"strconv"
	"time"
)

// kinds 全部邮件用途，供构造时校验配置完整性
var kinds = []Kind{KindVerifyEmail, KindBindEmail, KindResetPassword}

// subjectByKind 邮件用途到主题的映射，主题随请求提交，模板正文由服务商侧维护
var subjectByKind = map[Kind]string{
	KindVerifyEmail:   "验证你的 eChat 邮箱",
	KindBindEmail:     "绑定你的 eChat 邮箱",
	KindResetPassword: "重置你的 eChat 密码",
}

// subjectOf 取指定用途的邮件主题
// kind 邮件用途
// 返回主题；用途未知时返回错误
func subjectOf(kind Kind) (string, error) {
	subject, ok := subjectByKind[kind]
	if !ok {
		return "", fmt.Errorf("未知邮件用途 %q", kind)
	}
	return subject, nil
}

// withYear 返回模板变量的副本，缺失 year 时补当前年份
// vars 原始变量表，可为 nil
// 返回补全年份后的新变量表，不修改入参
func withYear(vars map[string]string) map[string]string {
	data := make(map[string]string, len(vars)+1)
	for k, v := range vars {
		data[k] = v
	}
	if data["year"] == "" {
		data["year"] = strconv.Itoa(time.Now().Year())
	}
	return data
}
