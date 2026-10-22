package scenario

import (
	"time"

	"echat-loadtest/internal/metrics"
)

// InterruptParams 打断层场景参数：会话层负载 + 高频 mock ASR 产出
//
// 打断不是客户端消息（docs/loadtest.md §1.1）：mock ASR 的产出间隔短于 AI 播出时长时，
// 句末结果落在播放窗口内，自然触发服务端 barge-in（asrdispatch → CancelTurn + TTS Interrupt）。
// 客户端可观测信号是 burst 变短、burst 频率升高；打断响应延迟（ASR 结果→TTS 停播）
// 由服务端 TurnInterruptDelay 直方图采集，报告中与客户端曲线交叉印证。
type InterruptParams struct {
	SessionParams
}

// RunInterrupt 打断层场景：与 session 场景同构，仅默认参数更激进。
// 推荐配置：ASR 产出间隔 1.2s（短于一句回复的播出时长），hold 2m。
func RunInterrupt(p InterruptParams) *metrics.Report {
	if p.ASRInterval <= 0 {
		p.ASRInterval = 1200 * time.Millisecond
	}
	if p.Hold <= 0 {
		p.Hold = 2 * time.Minute
	}
	rep := RunSession(p.SessionParams)
	// 场景名与 run_id 改标为 interrupt，报告与 results 文件名可区分
	rep.Scenario = "interrupt"
	rep.RunID = "interrupt" + trimPrefix(rep.RunID, "session")
	return rep
}

// trimPrefix strings.TrimPrefix 的局部别名，避免为一次调用引入 strings 包
func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
