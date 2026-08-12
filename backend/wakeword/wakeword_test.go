package wakeword

import "testing"

// TestIsWake 验证唤醒词同音近音命中与无关词不命中
func TestIsWake(t *testing.T) {
	hitCases := []string{
		"小月",
		"小岳",
		"小悦",
		"小玥",
		"小越",
		"小月同学",
		"小月，你好",
	}
	for _, c := range hitCases {
		if !IsWake(c) {
			t.Errorf("期望命中唤醒词，实际未命中: %q", c)
		}
	}

	missCases := []string{
		"今天天气怎么样",
		"学校",
		"小夜",
		"洗衣服",
	}
	for _, c := range missCases {
		if IsWake(c) {
			t.Errorf("期望不命中唤醒词，实际命中: %q", c)
		}
	}
}

// TestIsSleep 验证休眠词同音近音命中与无关词不命中
func TestIsSleep(t *testing.T) {
	hitCases := []string{
		"小月再见",
		"小月拜拜",
		"小月，再见",
	}
	for _, c := range hitCases {
		if !IsSleep(c) {
			t.Errorf("期望命中休眠词，实际未命中: %q", c)
		}
	}

	missCases := []string{
		"小月",
		"再见",
		"今天天气怎么样",
	}
	for _, c := range missCases {
		if IsSleep(c) {
			t.Errorf("期望不命中休眠词，实际命中: %q", c)
		}
	}
}
