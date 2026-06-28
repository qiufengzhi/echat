package sfu

import (
	"echat-backend/asr_cli"
	"echat-backend/vad_cli"
	"fmt"
	"log"
	"sync"

	asrpb "echat-backend/proto/asr"

	"github.com/pion/opus"
	"github.com/pion/rtp"
)

// vadBufferSize = 16000Hz * 0.5s = 8000 samples，攒够 500ms 后送给 VAD
const vadBufferSize = 8000

// asrPush asr服务请求参数
type asrReq struct {
	packet   *rtp.Packet // RTP 包
	clientID string      // 客户端id，等同于用户id
	roomId   string      // 房间id
}

func init() {
	// todo 本地部署asr接受识别结果用
	return
	go getAsrRes()
}

func getAsrRes() {
	for res := range asr_cli.AsrServiceClient.Out {
		log.Printf("[asr] 收到识别结果: user=%s text=%q isFinal=%v seq=%d", res.ClientId, res.Text, res.IsFinal, res.Seq)
	}
}

// asrChunkDuration samples = 16000Hz * 0.4s = 6400 samples，攒够 400ms 送一次 ASR
const asrChunkDuration = 6400

// asrBuffer 每个 session 的 ASR PCM 缓冲
type asrBuffer struct {
	pcm []int16
	mu  sync.Mutex
	seq int64
}

// asrBuffers 按 sessionId 缓存待发送的 PCM
var asrBuffers = &sync.Map{} // map[sessionId]*asrBuffer

// speechRecognition 语音识别：解码 Opus → VAD 过滤静音 → 攒帧 → 发 ASR
func speechRecognition(ap asrReq) {
	opusData := ap.packet.Payload
	if len(opusData) < 2 {
		return
	}

	pcm, err := decodeOpusToInt16(opusData, 16000)
	if err != nil {
		log.Printf("[asr] 解码失败: %v", err)
		return
	}

	// VAD 检测当前帧是否为人声，静音跳过，只把说话片段送 ASR
	sessionId := getSessionId(ap.clientID, ap.roomId)
	if !vadDetectAndAccumulate(pcm, sessionId) {
		return
	}

	buf := getOrCreateASRBuffer(sessionId)
	buf.mu.Lock()
	buf.pcm = append(buf.pcm, pcm...)

	// 未攒够 400ms，先不发送
	if len(buf.pcm) < asrChunkDuration {
		buf.mu.Unlock()
		return
	}

	// 取出一段 400ms 送 ASR
	chunk := make([]int16, asrChunkDuration)
	copy(chunk, buf.pcm[:asrChunkDuration])
	buf.pcm = buf.pcm[asrChunkDuration:]
	buf.seq++
	seq := buf.seq
	buf.mu.Unlock()

	asr_cli.AsrServiceClient.In <- asrpb.AudioChunk{
		SessionId:  sessionId,
		RoomId:     ap.roomId,
		ClientId:   ap.clientID,
		Pcm:        int16ToLEBytes(chunk),
		SampleRate: 16000,
		IsLast:     false,
		Seq:        seq,
	}
}

func getOrCreateASRBuffer(sessionID string) *asrBuffer {
	v, _ := asrBuffers.LoadOrStore(sessionID, &asrBuffer{})
	return v.(*asrBuffer)
}

// RemoveASRBuffer 客户端离开时清理对应 session 缓冲，并发送结束包
func RemoveASRBuffer(sessionID string) {
	v, ok := asrBuffers.LoadAndDelete(sessionID)
	if !ok {
		return
	}
	buf := v.(*asrBuffer)
	buf.mu.Lock()
	defer buf.mu.Unlock()

	// 发送最后一段（如果有残留数据）
	if len(buf.pcm) > 0 {
		buf.seq++
		asr_cli.AsrServiceClient.In <- asrpb.AudioChunk{
			SessionId:  sessionID,
			Pcm:        int16ToLEBytes(buf.pcm),
			SampleRate: 16000,
			IsLast:     true,
			Seq:        buf.seq,
		}
	} else {
		// 没有数据也发结束标记，让 ASR 服务刷新内部缓存
		buf.seq++
		asr_cli.AsrServiceClient.In <- asrpb.AudioChunk{
			SessionId:  sessionID,
			SampleRate: 16000,
			IsLast:     true,
			Seq:        buf.seq,
		}
	}
}

// vadState 每个 session 的 VAD 跟踪状态
type vadState struct {
	pcm        []int16
	isSpeaking bool // 当前是否在说话（由上次 VAD 检测结果决定）
	mu         sync.Mutex
}

// vadStates 按 sessionId 跟踪每个会话的说话状态
var vadStates = &sync.Map{} // map[sessionId]*vadState

// vadDetectAndAccumulate 攒够 500ms 做一次 VAD 判定，决定是否继续放行
// 返回 true 表示当前帧可能是人声（或尚未判定），应送 ASR
// 初始状态放行，直到 VAD 判定为静音后才拦截；再次检测到人声时恢复放行
func vadDetectAndAccumulate(pcm []int16, sessionID string) bool {
	vs := getOrCreateVADState(sessionID)
	vs.mu.Lock()
	vs.pcm = append(vs.pcm, pcm...)

	// 尚未攒够 500ms：保持当前状态
	if len(vs.pcm) < vadBufferSize {
		cur := vs.isSpeaking
		vs.mu.Unlock()
		return cur
	}

	// 攒够了，取出检测
	chunk := make([]int16, vadBufferSize)
	copy(chunk, vs.pcm[:vadBufferSize])
	vs.pcm = vs.pcm[vadBufferSize:]
	vs.mu.Unlock()

	hasVoice, _, err := vad_cli.VADClientInstance.Detect(chunk, 16000)
	if err != nil {
		log.Printf("[vad] 检测失败: %v", err)
		// 出错时保守放行
		return true
	}

	vs.mu.Lock()
	vs.isSpeaking = hasVoice
	vs.mu.Unlock()

	//log.Printf("[vad] session=%s hasVoice=%v", sessionID[:8], hasVoice)
	return hasVoice
}

func getOrCreateVADState(sessionID string) *vadState {
	v, _ := vadStates.LoadOrStore(sessionID, &vadState{isSpeaking: true}) // 初始放行，首次检测后再截断
	return v.(*vadState)
}

// RemoveVADState 客户端离开时清理 VAD 状态
func RemoveVADState(sessionID string) {
	vadStates.Delete(sessionID)
}

// 生成一个唯一的 sessionId。房间+用户 视为同一个会话
func getSessionId(clientID string, roomId string) string {
	sessionId := fmt.Sprintf("%s-%s", clientID, roomId)
	return sessionId
}

func decodeOpusToInt16(opusData []byte, sampleRate int) ([]int16, error) {
	// 创建解码器，默认单声道
	dec, err := opus.NewDecoderWithOutput(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("创建解码器失败: %w", err)
	}

	// 分配足够大的缓冲区（一帧最大 120ms @16kHz = 1920 samples）
	pcm := make([]int16, 320*6) // 20ms @16kHz = 320 samples，乘6兜住大帧
	n, err := dec.DecodeToInt16(opusData, pcm)
	if err != nil {
		return nil, fmt.Errorf("解码失败: %w", err)
	}

	return pcm[:n], nil
}

// int16ToLEBytes 将 []int16 转成小端序 byte 切片
func int16ToLEBytes(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, v := range samples {
		buf[i*2] = byte(v)
		buf[i*2+1] = byte(v >> 8)
	}
	return buf
}
