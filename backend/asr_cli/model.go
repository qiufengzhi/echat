package asr_cli

import (
	"sync"
	"time"

	asrpb "echat-backend/proto/asr"

	nls "github.com/aliyun/alibabacloud-nls-go-sdk"
)

// ---------- 配置 ----------

// AliyunASRConfig 阿里云 ASR 连接配置。
// 优先从环境变量读取，也可在代码中直接赋值。
type AliyunASRConfig struct {
	AccessKeyID                 string        // 阿里云账号 AccessKey ID，为空时从环境变量 ALIBABA_CLOUD_ACCESS_KEY_ID 读取
	AccessKeySecret             string        // 阿里云账号 AccessKey Secret，为空时从环境变量 ALIBABA_CLOUD_ACCESS_KEY_SECRET 读取
	AppKey                      string        // 智能语音交互项目 AppKey，为空时从环境变量 NLS_APP_KEY 读取
	URL                         string        // 实时识别 WebSocket 地址，默认 nls.DEFAULT_URL
	SampleRate                  int           // 采样率，默认 16000
	EnableIntermediateResult    bool          // 是否返回中间结果，默认 true
	EnablePunctuationPrediction bool          // 是否启用标点预测，默认 true
	EnableITN                   bool          // 是否启用中文数字转阿拉伯数字，默认 true
	MaxSentenceSilence          int           // 断句静音阈值（毫秒），范围 200~2000，默认 800
	SessionIdleTimeout          time.Duration // session 无数据多久后自动关闭，默认 30s
	TokenRefreshInterval        time.Duration // Token 刷新间隔，默认 0 表示不主动刷新（SDK 内部会处理过期）
}

// ---------- 会话级别结果 ----------

// sessionResult 单个 session 的识别结果（用于 outputFan → AudioOut 通道）。
type sessionResult struct {
	SessionID string // 对应音频块的 session_id，区分不同语音会话
	RoomID    string // 对应音频块的 room_id
	ClientID  string // 对应音频块的 client_id
	Text      string // 识别文本内容
	IsFinal   bool   // 是否为该句话的最终结果
	Seq       int64  // 对应音频块的序号，-1 表示错误消息
}

// ---------- 单个 session 的连接管理 ----------

// asrSession 封装一个阿里云 SpeechTranscription 实例及生命周期。
type asrSession struct {
	id        string                   // session_id
	roomID    string                   // 所属房间 ID
	clientID  string                   // 说话客户端 ID
	trans     *nls.SpeechTranscription // 阿里云实时语音识别实例
	sdkLogger *nls.NlsLogger           // SDK 内部日志器（静默），传给 nls.NewSpeechTranscription 使用
	out       chan sessionResult       // SDK 回调收到的识别文本写入此 channel，由 Recognizer 的 outputFan 转发到 AudioOut
	lastAudio time.Time                // 最后一次收到音频的时间，用于空闲超时清理
	closeFunc func()                   // 关闭此 session 的回调函数
	mu        sync.Mutex               // 保护 lastAudio 的并发访问
}

// ---------- 识别器 ----------

// Recognizer 阿里云实时语音识别器。
// 对外提供 AudioIn / AudioOut 两个通道。
type Recognizer struct {
	cfg AliyunASRConfig // ASR 连接配置

	// 输入通道：发送 AudioChunk 给阿里云 ASR，关闭此 channel 会停止所有 session
	AudioIn chan asrpb.AudioChunk

	// 输出通道：读取 TranscriptAudioChunk 识别结果，所有 session 清理完毕后关闭
	AudioOut chan *asrpb.TranscriptAudioChunk

	// ---- 内部状态 ----
	sessions map[string]*asrSession // 活跃 session map，key=session_id
	mu       sync.Mutex             // 保护 sessions map 的并发访问
	//stopCh    chan struct{}          // 通知 idleCleaner 协程退出
	sdkLogger *nls.NlsLogger     // SDK 内部日志器（静默）
	outputFan chan sessionResult // 汇总各 session 结果，单协程写入 AudioOut
}
