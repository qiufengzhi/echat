// stats.go 系统层与业务层指标的记录辅助：把 metric/attribute 细节收敛在本包
package observability

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// IncAgentTimedOut 记一次轮次超时
func IncAgentTimedOut() { AgentTimedOut.Add(recordContext, 1) }

// IncAgentFailed 记一次轮次异常结束
func IncAgentFailed() { AgentFailed.Add(recordContext, 1) }

// IncAgentRejected 记一次在途未释放被放弃
func IncAgentRejected() { AgentRejected.Add(recordContext, 1) }

// IncAgentCancelled 记一次主动取消
func IncAgentCancelled() { AgentCancelled.Add(recordContext, 1) }

// IncAgentPreempted 记一次同会话新轮抢占
func IncAgentPreempted() { AgentPreempted.Add(recordContext, 1) }

// RecordRooms 记录当前 SFU 房间数
//
// n: 房间数
func RecordRooms(n int) { RoomsActive.Record(recordContext, int64(n)) }

// RecordPeers 记录当前 SFU 连接数
//
// n: 连接数
func RecordPeers(n int) { PeersActive.Record(recordContext, int64(n)) }

// IncRenegotiation 记一次 SFU 重新协商
func IncRenegotiation() { Renegotiations.Add(recordContext, 1) }

// AddTTSRequest 记一次 TTS 合成请求
//
// provider: 合成提供商，取值 aliyun | xfyun
// status: 结果，取值 ok | error
func AddTTSRequest(provider, status string) {
	TtsRequests.Add(recordContext, 1, metric.WithAttributes(
		attribute.String("provider", provider), attribute.String("status", status)))
}

// AddTTSCharacters 记 TTS 合成字符数
//
// provider: 合成提供商
// n: 字符数
func AddTTSCharacters(provider string, n int) {
	if n <= 0 {
		return
	}
	TtsCharacters.Add(recordContext, int64(n), metric.WithAttributes(attribute.String("provider", provider)))
}

// AddASRAudioSeconds 记送识别的音频秒数
//
// ms: 本次音频时长毫秒
func AddASRAudioSeconds(ms int64) {
	if ms <= 0 {
		return
	}
	AsrAudioSeconds.Add(recordContext, ms/1000)
}

// RecordFirstAudioEncode 记录本轮首块音频到首帧 RTP 写出的间隔
//
// ms: 毫秒
func RecordFirstAudioEncode(ms int64) {
	if ms >= 0 {
		TurnFirstAudioEncode.Record(recordContext, ms)
	}
}
