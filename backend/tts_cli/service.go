// Package tts 语音合成模块
//
// service.go 定义 TTS 服务入口，是 tts 包对外的完整服务层
// 负责接收 LLM 流式输出 → 断句 → 送入合成管道 → 处理用户打断
package tts_cli

import (
	"strings"
	"sync"
	"unicode/utf8"

	"echat-backend/proto/llm"
)

// sentenceEndChars 句子结束符，遇到这些字符时断句
var sentenceEndChars = []string{"。", "！", "？", ".", "!", "?"}

// skipLeadingPartialRunes 跳过字符串开头的非法 UTF-8 字节（多字节字符被 gRPC 流拆分时
// 会形成前导 continuation bytes，这些字节不构成有效字符，保留会导致讯飞合成噪音）
func skipLeadingPartialRunes(s string) string {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			break
		}
		s = s[1:]
	}
	return s
}

// Service TTS 服务入口，串联断句、合成与打断
//
// 充当 tts 包对外的完整服务层：
//   - 接收 LLM 流式 token，按句结束符切分完整句子
//   - 将完整句子送入底层 TTS 合成管道（aliyun/xfyun）
//   - 处理用户打断（barge-in），取消进行中的合成任务
type Service struct {
	buffer map[string]*sentenceBuffer // sessionID -> 缓冲区
	mu     sync.RWMutex               // 保护 buffer map 的增删查，不护业务处理
}

// sentenceBuffer 每个 session 的文本缓冲区
type sentenceBuffer struct {
	mu       sync.Mutex       // 保护本会话的文本缓冲，只被同会话的调用竞争
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
	}
}

// getOrCreate 取会话缓冲，不存在则创建（含其 TTS 管道）
//
// 只在读写缓冲区表时持锁，返回后立即释放，不同会话之间不互相阻塞
// resp: 本轮响应，取其会话 ID、房间与客户端信息
// return: 该会话的缓冲指针
func (ss *Service) getOrCreate(resp *llm.TurnResponse) *sentenceBuffer {
	sessionID := resp.SessionId
	ss.mu.RLock()
	sb, ok := ss.buffer[sessionID]
	ss.mu.RUnlock()
	if ok {
		return sb
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if sb, ok = ss.buffer[sessionID]; ok {
		return sb
	}
	sp, exists := provider.GetSession(sessionID)
	if !exists {
		sp = provider.CreateSession(sessionID)
	}
	sb = &sentenceBuffer{
		roomID:   resp.RoomId,
		clientID: resp.ClientId,
		seq:      resp.Seq,
		sp:       sp,
	}
	ss.buffer[sessionID] = sb
	return sb
}

// ProcessText 处理 LLM 的流式响应
// 将 token 累积到缓冲区，遇到句结束符时送入 TTS
// 同一会话内串行调用，跨会话各持自己的缓冲锁，一个会话慢不阻塞其他会话
func (ss *Service) ProcessText(resp *llm.TurnResponse) {
	sb := ss.getOrCreate(resp)
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sessionID := resp.SessionId

	// 跳过前导非法 UTF-8 字节（上一段字符被 gRPC 流拆分的尾巴），再累积
	sb.text.WriteString(skipLeadingPartialRunes(resp.ResponseText))

	// 循环检查是否可以断句
	for {
		sentence, remainder, found := splitAtSentenceEnd(sb.text.String())
		if !found {
			break // 没有找到句结束符，继续累积
		}

		// 将完整句子送入 TTS（synthesizeSentence 里还会再做 ToValidUTF8 兜底）
		if strings.TrimSpace(sentence) != "" {
			if sb.sp.SendSentence(sentence) {
				logger.Infow("送入 TTS", "sessionID", sessionID, "sentence", sentence)
			} else {
				logger.Warnw("TTS 队列已满，丢弃句子", "sessionID", sessionID, "sentence", sentence)
			}
		}

		// 重置缓冲区，保留剩余文本（去除前导非法字节）
		sb.text.Reset()
		sb.text.WriteString(skipLeadingPartialRunes(remainder))
	}

	// LLM 回复结束，将剩余文本作为最后一句送入 TTS
	if resp.IsFinal {
		if strings.TrimSpace(sb.text.String()) != "" {
			if sb.sp.SendSentence(sb.text.String()) {
				logger.Infow("最后一句送入 TTS", "sessionID", sessionID, "sentence", sb.text.String())
			} else {
				logger.Warnw("最后一句队列已满，丢弃", "sessionID", sessionID)
			}
		}
		ss.mu.Lock()
		delete(ss.buffer, sessionID)
		ss.mu.Unlock()
		logger.Infow("会话结束，清理缓冲区", "sessionID", sessionID)
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
	logger.Infow("用户打断，已清理所有数据", "sessionID", sessionID)
}

// GetAudio 获取合成的音频输出通道
//
// resp: 本轮响应，取其会话 ID 定位缓冲
// return: 该会话的合成音频输出通道
func (ss *Service) GetAudio(resp *llm.TurnResponse) <-chan []byte {
	return ss.getOrCreate(resp).sp.AudioCh()
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
