// voice.go 本地能量判停与边缘指标记录器（媒体面 RTCP、客户端播放延迟）
//
// 能量判停：服务端拿不到云端 ASR 的「用户停止说话」时刻，用最后一帧超阈值语音的本地时间戳近似
package observability

import (
	"sync"
	"time"
)

// rmsThreshold int16 域能量门限，均方值超过它视为有语音（约 -36 dBFS 量级）
const rmsThreshold = 500

// voiceReg 会话 -> 最近一次有效语音帧的 UnixMilli
var voiceReg = struct {
	sync.Mutex
	m map[string]int64
}{m: make(map[string]int64)}

// HasVoice 判断一段 int16 PCM 是否超过能量门限
//
// pcm: 解码后的 16 位有符号采样
// return: 超过门限返回 true
func HasVoice(pcm []int16) bool {
	if len(pcm) == 0 {
		return false
	}
	var sum int64
	for _, s := range pcm {
		sum += int64(s) * int64(s)
	}
	mean := sum / int64(len(pcm))
	return mean > int64(rmsThreshold)*int64(rmsThreshold)
}

// MarkVoiceFrame 记录会话最近一次有语音的时刻，作为本地判停锚点
//
// sessionID: 会话标识
func MarkVoiceFrame(sessionID string) {
	voiceReg.Lock()
	voiceReg.m[sessionID] = time.Now().UnixMilli()
	voiceReg.Unlock()
}

// MarkASRFinal 收到句末 final 时记 asr_final_delay = 此刻 - 最近一次有效语音帧
//
// 只在该会话有判停锚点时记录（无锚点说明没测到本地语音，不计）
// sessionID: 会话标识
func MarkASRFinal(sessionID string) {
	voiceReg.Lock()
	last, ok := voiceReg.m[sessionID]
	if ok {
		delete(voiceReg.m, sessionID)
	}
	voiceReg.Unlock()
	if !ok {
		return
	}
	TurnASRFinalDelay.Record(recordContext, time.Now().UnixMilli()-last)
}

// ClearVoice 会话结束时清理判停锚点，避免状态泄漏
//
// sessionID: 会话标识
func ClearVoice(sessionID string) {
	voiceReg.Lock()
	delete(voiceReg.m, sessionID)
	voiceReg.Unlock()
}

// RecordRTCP 记录一条 RTCP Receiver Report 的抖动（微秒）与丢包率（百分比）
//
// jitterUs: 到达间隔抖动，微秒
// lossPercent: 丢包率，0-100
func RecordRTCP(jitterUs int64, lossPercent int64) {
	if jitterUs >= 0 {
		RTCPJitterUs.Record(recordContext, jitterUs)
	}
	if lossPercent >= 0 {
		RTCPLossPercent.Record(recordContext, lossPercent)
	}
}

// RecordClientPlayback 记录客户端上报的播放延迟（毫秒）
//
// ms: 客户端 getStats 得到的时长差值，负数忽略
func RecordClientPlayback(ms int64) {
	if ms >= 0 {
		ClientPlaybackDelayMs.Record(recordContext, ms)
	}
}
