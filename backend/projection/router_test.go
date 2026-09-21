package projection

import (
	"testing"
	"time"
)

// TestNakDelayBacksOffAndCaps 校验重投延迟按投递次数翻倍并封顶：
// 首次失败 1s，其后 2s/4s/8s，达到上限后保持 30s 不再增长
func TestNakDelayBacksOffAndCaps(t *testing.T) {
	want := []time.Duration{
		1 * time.Second,  // 第 1 次投递失败
		2 * time.Second,  // 第 2 次
		4 * time.Second,  // 第 3 次
		8 * time.Second,  // 第 4 次
		16 * time.Second, // 第 5 次
		30 * time.Second, // 第 6 次起封顶
		30 * time.Second,
		30 * time.Second,
	}
	for i, w := range want {
		delivered := uint64(i + 1)
		if got := nakDelay(delivered); got != w {
			t.Fatalf("第 %d 次投递失败后延迟=%v 期望=%v", delivered, got, w)
		}
	}
}

// TestNakDelayNeverZero 退避延迟必须为正，否则等价于立即重投（热循环）
func TestNakDelayNeverZero(t *testing.T) {
	for delivered := uint64(0); delivered <= 64; delivered++ {
		if d := nakDelay(delivered); d <= 0 {
			t.Fatalf("投递次数 %d 时延迟=%v，不应为非正值", delivered, d)
		}
	}
}
