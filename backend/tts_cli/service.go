// Package tts 语音合成模块
//
// service.go 定义 TTS 服务入口，是 tts 包对外的完整服务层
// 负责接收 LLM 流式输出 → 断句 → 送入合成管道 → 处理用户打断
package tts_cli

import (
	"log"
	"strings"
	"sync"

	"echat-backend/proto/llm"
)

// sentenceEndChars 句子结束符，遇到这些字符时断句
var sentenceEndChars = []string{"。", "！", "？", ".", "!", "?"}

// Service TTS 服务入口，串联断句、合成与打断
//
// 充当 tts 包对外的完整服务层：
//   - 接收 LLM 流式 token，按句结束符切分完整句子
//   - 将完整句子送入底层 TTS 合成管道（aliyun/xfyun）
//   - 处理用户打断（barge-in），取消进行中的合成任务
type Service struct {
	buffer map[string]*sentenceBuffer // sessionID -> 缓冲区
	mu     sync.Mutex                 // 保护 buffer map
	logger *log.Logger                // 日志
}

// sentenceBuffer 每个 session 的文本缓冲区
type sentenceBuffer struct {
	text     strings.Builder  // 累积的文本
	roomID   string           // 房间 ID
	clientID string           // 客户端 ID
	seq      int64            // 请求序号
	sp       *SessionPipeline // 关联的 TTS 管道
}

// GlobalTTSService 全局 TTS 服务实例，init() 自动初始化
var GlobalTTSService *Service

func init() {
	GlobalTTSService = &Service{
		buffer: make(map[string]*sentenceBuffer),
		logger: log.Default(),
	}
}

// ProcessText 处理 LLM 的流式响应
// 将 token 累积到缓冲区，遇到句结束符时送入 TTS
func (ss *Service) ProcessText(resp *llm.LLMResponse) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	sessionID := resp.SessionId

	// 如果该 session 还没有缓冲区，创建一个
	if _, ok := ss.buffer[sessionID]; !ok {
		sp, exists := provider.GetSession(sessionID)
		if !exists {
			sp = provider.CreateSession(sessionID)
		}

		ss.buffer[sessionID] = &sentenceBuffer{
			roomID:   resp.RoomId,
			clientID: resp.ClientId,
			seq:      resp.Seq,
			sp:       sp,
		}
	}

	sb := ss.buffer[sessionID]

	// 将当前 token 添加到缓冲区
	sb.text.WriteString(resp.ResponseText)

	// 循环检查是否可以断句
	for {
		sentence, remainder, found := splitAtSentenceEnd(sb.text.String())
		if !found {
			break // 没有找到句结束符，继续累积
		}

		// 将完整句子送入 TTS
		if strings.TrimSpace(sentence) != "" {
			if sb.sp.SendSentence(sentence) {
				ss.logger.Printf("[tts:service][%s] 送入 TTS: %s", sessionID, sentence)
			} else {
				ss.logger.Printf("[tts:service][%s] TTS 队列已满，丢弃句子: %s", sessionID, sentence)
			}
		}

		// 重置缓冲区，保留剩余文本
		sb.text.Reset()
		sb.text.WriteString(remainder)
	}

	// LLM 回复结束，将剩余文本作为最后一句送入 TTS
	if resp.IsFinal {
		if strings.TrimSpace(sb.text.String()) != "" {
			if sb.sp.SendSentence(sb.text.String()) {
				ss.logger.Printf("[tts:service][%s] 最后一句送入 TTS: %s", sessionID, sb.text.String())
			} else {
				ss.logger.Printf("[tts:service][%s] 最后一句队列已满，丢弃", sessionID)
			}
		}
		delete(ss.buffer, sessionID)
		ss.logger.Printf("[tts:service][%s] 会话结束，清理缓冲区", sessionID)
	}
}

// BargeIn 用户打断（barge-in）时清理该 session 的所有数据
//
// 取消正在进行的合成任务，丢弃待合成句子
func (ss *Service) BargeIn(sessionID string) {
	ss.mu.Lock()
	sb, ok := ss.buffer[sessionID]
	if ok {
		delete(ss.buffer, sessionID)
	}
	ss.mu.Unlock()

	if ok && sb.sp != nil {
		sb.sp.Cancel()
	}

	provider.RemoveSession(sessionID)
	ss.logger.Printf("[tts:service][%s] 用户打断，已清理所有数据", sessionID)
}

// GetAudio 获取合成的音频输出通道
func (ss *Service) GetAudio(sessionID string) <-chan []byte {
	return ss.buffer[sessionID].sp.AudioCh()
}

// splitAtSentenceEnd 在文本中查找第一个句结束符，将文本分割为句子和剩余部分
func splitAtSentenceEnd(text string) (sentence, remainder string, found bool) {
	minIdx := -1
	for _, char := range sentenceEndChars {
		if idx := strings.Index(text, char); idx != -1 {
			if minIdx == -1 || idx < minIdx {
				minIdx = idx
			}
		}
	}

	if minIdx == -1 {
		return "", text, false
	}

	return text[:minIdx+1], text[minIdx+1:], true
}
