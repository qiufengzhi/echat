package sfu

import (
	"echat-backend/vad"
	"fmt"
	"github.com/pion/opus"
	"github.com/pion/rtp"
	"log"
	"sync"
)

// vadBufferSize = 16000Hz * 0.5s = 8000 samples，攒够 500ms 后送给 VAD
const vadBufferSize = 8000

// userVADBuffer 每个用户的 VAD 缓冲状态
type userVADBuffer struct {
	pcm []int16
	mu  sync.Mutex
}

// vadBuffers 按 clientID 缓存 PCM，键是 sourceID（说话人）
var vadBuffers = &sync.Map{} // map[clientID]*userVADBuffer

// vadDetect 检测 RTP 数据包是否包含人声，返回是否有声音（bool）
func vadDetect(packet *rtp.Packet, clientID string, roomId string) (hasVoice bool) {
	// 1. 获取 Opus 载荷
	opusData := packet.Payload

	// DTX / Comfort Noise / 空包等不可解码的载荷，跳过解码，视为无人声
	if len(opusData) < 2 {
		return
	}

	// 2. 解码为 int16 PCM（silero-vad 原生 16kHz）
	pcm, err := decodeOpusToInt16(opusData, 16000)
	if err != nil {
		log.Printf("[vad] 解码失败: %v", err)
		return
	}

	// 3. 写入缓冲区
	sessionId := getSessionId(clientID, roomId)
	buf := getOrCreateVADBuffer(sessionId)
	buf.mu.Lock()
	buf.pcm = append(buf.pcm, pcm...)

	// 未攒够 500ms，返回之前的状态（保守策略：不够时不丢弃，视为有声音）
	if len(buf.pcm) < vadBufferSize {
		buf.mu.Unlock()
		return true // 攒不够时放行，避免因缓冲时间差导致漏掉声音
	}

	// 4. 取出一段 500ms 数据送 VAD
	chunk := make([]int16, vadBufferSize)
	copy(chunk, buf.pcm[:vadBufferSize])
	buf.pcm = buf.pcm[vadBufferSize:] // 保留新的剩余部分
	buf.mu.Unlock()

	hasVoice, score, err := vad.VADClientInstance.Detect(chunk, 16000)
	if err != nil {
		log.Printf("[vad] VAD检测失败: %v", err)
		return true // 保守放行
	}
	log.Printf("[vad] client=%s hasVoice=%v score=%.2f", clientID[:8], hasVoice, score)
	return
}

func getOrCreateVADBuffer(clientID string) *userVADBuffer {
	v, _ := vadBuffers.LoadOrStore(clientID, &userVADBuffer{})
	return v.(*userVADBuffer)
}

// 生成一个唯一的 sessionId。房间+用户 视为同一个会话
func getSessionId(clientID string, roomId string) string {
	sessionId := fmt.Sprintf("%s-%s", clientID, roomId)
	return sessionId
}

// RemoveVADBuffer 在客户端离开时清理对应 buffer
func RemoveVADBuffer(clientID string) {
	vadBuffers.Delete(clientID)
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
