// Package wakeword 实现唤醒词与休眠词的拼音匹配
//
// 匹配思路：ASR 的错字几乎都是同音/近音字（"小月"被听成"小岳""小悦"）
// 字形不同但读音一致。因此把 ASR 文本转成拼音后做等价集包含匹配，
// 用读音作为匹配锚点，而非字形
package wakeword

import (
	"regexp"
	"strings"

	"github.com/mozillazg/go-pinyin"
)

// wakeKeywordPinyin 唤醒词「小月」的拼音等价集，归一化后无音调无分隔
var wakeKeywordPinyin = []string{
	"xiaoyue",        // 小月 / 小岳 / 小悦 / 小玥 / 小越
	"xiaoyuetongxue", // 小月同学
}

// sleepKeywordPinyin 休眠词「小月再见」的拼音等价集，归一化后无音调无分隔
var sleepKeywordPinyin = []string{
	"xiaoyuezaijian", // 小月再见
	"xiaoyuebaibai",  // 小月拜拜
}

// nonLetter 匹配所有非小写字母字符，归一化时移除标点、空格、数字
var nonLetter = regexp.MustCompile(`[^a-z]+`)

// toPinyin 把中文文本转成归一化拼音串：转小写、去音调、去标点空格
// 返回值是纯小写字母串，例如 "小月，再见" -> "xiaoyuezaijian"
func toPinyin(text string) string {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal // 无音调风格，ü 用 v 表示（如 "nv"），"月" 输出 "yue"

	parts := pinyin.LazyPinyin(text, args)
	joined := strings.ToLower(strings.Join(parts, ""))
	return nonLetter.ReplaceAllString(joined, "")
}

// match 判断归一化后的文本拼音是否包含等价集里任意一条
// text 是 ASR 识别文本，keywords 是等价集，命中返回 true
func match(text string, keywords []string) bool {
	p := toPinyin(text)
	for _, kw := range keywords {
		if strings.Contains(p, kw) {
			return true
		}
	}
	return false
}

// IsWake 判断文本是否为唤醒词，等价于「小月」等近音词
func IsWake(text string) bool {
	return match(text, wakeKeywordPinyin)
}

// IsSleep 判断文本是否为休眠词，等价于「小月再见」等近音词
func IsSleep(text string) bool {
	return match(text, sleepKeywordPinyin)
}
