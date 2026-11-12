// voice.go 本地能量判停与边缘指标记录器（媒体面 RTCP、客户端播放延迟）
//
// 能量判停：服务端拿不到云端 ASR 的「用户停止说话」时刻，用最后一帧超阈值语音的本地时间戳近似
package observability

import (
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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
// kind: 出流类型，relay 表示中继轨、ai 表示 AI TTS 轨
// jitterUs: 到达间隔抖动，微秒
// lossPercent: 丢包率，0-100
func RecordRTCP(kind string, jitterUs int64, lossPercent int64) {
	attrs := metric.WithAttributes(attribute.String("kind", kind))
	if jitterUs >= 0 {
		RTCPJitterUs.Record(recordContext, jitterUs, attrs)
	}
	if lossPercent >= 0 {
		RTCPLossPercent.Record(recordContext, lossPercent, attrs)
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

// RecordRTT 记录由 RTCP SR 与 RR 往返计算出的 RTT
//
// ms: 往返时延毫秒，负数忽略
func RecordRTT(ms int64) {
	if ms >= 0 {
		SFURttMs.Record(recordContext, ms)
	}
}

// RecordTxBytes 记录一次下行写出的字节数
//
// kind: 出流类型，relay 表示中继轨、ai 表示 AI TTS 轨
// n: 本次写出的字节数，小于等于 0 忽略
func RecordTxBytes(kind string, n int64) {
	if n > 0 {
		SFUTxBytes.Add(recordContext, n, metric.WithAttributes(attribute.String("kind", kind)))
	}
}

// RecordPeerDisconnect 记录一次对端非正常断开
//
// state: 连接状态字符串，取 disconnected 或 failed
func RecordPeerDisconnect(state string) {
	PeerDisconnects.Add(recordContext, 1, metric.WithAttributes(attribute.String("state", state)))
}

// RecordTwccFeedback 记录收到一个 TWCC 反馈包，用于判断下行反馈链是否通
func RecordTwccFeedback() {
	TwccFeedback.Add(recordContext, 1)
}

// RecordBurstLoss 记录一次连续丢包事件
//
// run: 连续丢失的帧数，按 1、2、3plus 三档分桶上报
func RecordBurstLoss(run int) {
	if run <= 0 {
		return
	}
	bucket := "3plus"
	switch run {
	case 1:
		bucket = "1"
	case 2:
		bucket = "2"
	}
	BurstLossEvents.Add(recordContext, 1, metric.WithAttributes(attribute.String("len", bucket)))
}

// RecordProcessCPU 记录进程 CPU 使用率
//
// percent: 使用率百分比，负数表示本平台不支持采集，直接跳过
func RecordProcessCPU(percent int64) {
	if percent >= 0 {
		ProcessCPUPercent.Record(recordContext, percent)
	}
}
