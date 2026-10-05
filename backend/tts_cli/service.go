// Package tts 语音合成模块
//
// service.go 定义 TTS 服务入口，是 tts 包对外的完整服务层
// 负责接收 LLM 流式输出 → 断句 → 送入合成管道 → 处理用户打断
package tts_cli

import (
	"strings"
	"sync"
	"unicode/utf8"

	"echat-backend/dataplane"
	"echat-backend/proto/llm"
)

// sentenceEndChars 句子结束符，遇到这些字符时断句
var sentenceEndChars = []string{"。", "！", "？", ".", "!", "?"}

// staleTokenDropped 累计丢弃的过期轮次文本 token
//
// 取消一轮 RPC 是尽力而为，Python 侧可能已产出若干在途 token；
// 与 staleSegmentDropped 的区别是这里拦在文本入口，那些拦在音频出口
var staleTokenDropped = dataplane.NewCounter("tts.stale_token", 0)

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
//   - 处理用户打断（barge-in），结束当前轮并清理其数据
//
// 会话状态分两层：sentenceBuffer 是当前轮的文本缓冲，随轮次创建销毁；
// endpoint 是音频输出端点，与会话同寿命，跨轮次复用
type Service struct {
	// buffer sessionID -> 当前轮的文本缓冲，轮次结束后删除
	buffer map[string]*sentenceBuffer
	// endpoints sessionID -> 会话级音频输出端点，随 Close 销毁
	endpoints map[string]*endpoint
	// mu 保护 buffer 与 endpoints 两张表的增删查，不护业务处理
	mu sync.RWMutex
}

// sentenceBuffer 每个会话当前轮次的文本缓冲区
type sentenceBuffer struct {
	// mu 保护本轮的文本缓冲与送句，只被同会话的调用竞争
	mu sync.Mutex
	// text 本轮累积的待断句文本
	text strings.Builder
	// roomID 房间 ID
	roomID string
	// clientID 客户端 ID
	clientID string
	// seq 请求序号
	seq int64
	// turnID 本轮轮次标识，用于识别迟到 token
	turnID string
	// sp 本轮关联的 TTS 合成执行器
	sp *SessionPipeline
}

// GlobalTTSService 全局 TTS 服务实例，init() 自动初始化
var GlobalTTSService *Service

func init() {
	GlobalTTSService = &Service{
		buffer:    make(map[string]*sentenceBuffer),
		endpoints: make(map[string]*endpoint),
	}
}

// getOrCreate 取本轮文本缓冲，轮次变化或不存在时重建，必要时为新执行器挂泵
//
// 只在读写缓冲表时持锁，返回后立即释放，不同会话之间不互相阻塞
// 轮次变化时直接覆盖旧缓冲：旧轮的迟到 token 已在外层按作废轮次拦下，不会混进新轮
// resp: 本轮响应，取其会话 ID、房间、客户端与轮次
// ep: 该会话的音频输出端点
// return: 本轮文本缓冲
func (ss *Service) getOrCreate(resp *llm.TurnResponse, ep *endpoint) *sentenceBuffer {
	sessionID := resp.SessionId

	ss.mu.RLock()
	sb, ok := ss.buffer[sessionID]
	ss.mu.RUnlock()
	if ok && sb.turnID == resp.TurnId {
		return sb
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()

	if sb, ok = ss.buffer[sessionID]; ok && sb.turnID == resp.TurnId {
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
		turnID:   resp.TurnId,
		sp:       sp,
	}
	ss.buffer[sessionID] = sb
	ep.beginTurn(resp.TurnId)

	// 每个执行器只挂一个泵：两个协程会抢同一条输出通道
	if ep.attach(sp) {
		go pumpPipeline(ep, sp)
	}
	return sb
}

// pumpPipeline 把一条合成执行器的输出泵入会话端点
//
// 执行器随轮次销毁重建而端点不变：泵随执行器退出，下一轮为新执行器重新挂泵
// ep: 目标端点；sp: 本轮合成执行器
func pumpPipeline(ep *endpoint, sp *SessionPipeline) {
	for pcm := range sp.AudioCh() {
		ep.publishFrom(sp, pcm)
	}
}

// ProcessText 处理 LLM 的流式响应
//
// 将 token 累积到缓冲区，遇到句结束符时送入 TTS
// 同一会话内串行调用，跨会话各持自己的缓冲锁，一个会话慢不阻塞其他会话
// resp: 本轮的一个流式响应片段
func (ss *Service) ProcessText(resp *llm.TurnResponse) {
	ep := ss.AudioEndpoint(resp.SessionId)

	// 迟到 token：该轮已结束或被新轮取代，丢弃而不是混进当前回复
	if ep.isDead(resp.TurnId) {
		staleTokenDropped.Drop("sessionID", resp.SessionId, "turnID", resp.TurnId)
		return
	}

	sb := ss.getOrCreate(resp, ep)
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
		ep.markDead(resp.TurnId)

		ss.mu.Lock()
		delete(ss.buffer, sessionID)
		ss.mu.Unlock()
		logger.Infow("会话结束，清理缓冲区", "sessionID", sessionID)
	}
}

// Interrupt 用户打断（barge-in）：结束当前轮
//
// 顺序是刻意的：先把端点标记为无效并投递打断事件，让播放侧丢弃已缓冲的音频，
// 再销毁本轮的合成执行器。端点本身不销毁，下一轮在同一个端点上继续
// sessionID: 会话标识
func (ss *Service) Interrupt(sessionID string) {
	ss.mu.Lock()
	sb, hasBuffer := ss.buffer[sessionID]
	if hasBuffer {
		delete(ss.buffer, sessionID)
	}
	ep := ss.endpoints[sessionID]
	ss.mu.Unlock()

	if ep != nil {
		ep.interrupt()
	}
	if hasBuffer && sb.sp != nil {
		sb.sp.Cancel()
	}

	provider.RemoveSession(sessionID)
	logger.Infow("用户打断，已结束当前轮", "sessionID", sessionID)
}

// Close 关闭会话的全部 TTS 资源：文本缓冲、输出端点、合成执行器
//
// 供房间或连接销毁时调用；不调用会同时泄漏合成协程与运行快照条目
// sessionID: 会话标识
func (ss *Service) Close(sessionID string) {
	ss.mu.Lock()
	delete(ss.buffer, sessionID)
	ep, hasEndpoint := ss.endpoints[sessionID]
	if hasEndpoint {
		delete(ss.endpoints, sessionID)
	}
	ss.mu.Unlock()

	if hasEndpoint {
		ep.close()
	}
	provider.RemoveSession(sessionID)
}

// AudioEndpoint 返回会话的音频输出端点，不存在时创建
//
// 播放侧只订阅端点，不感知合成执行器的换代；端点随 Close 销毁
// sessionID: 会话标识
// return: 会话端点，同一会话多次调用返回同一实例
func (ss *Service) AudioEndpoint(sessionID string) *endpoint {
	ss.mu.RLock()
	ep, ok := ss.endpoints[sessionID]
	ss.mu.RUnlock()
	if ok {
		return ep
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ep, ok = ss.endpoints[sessionID]; ok {
		return ep
	}
	ep = newEndpoint(sessionID, defaultAudioBufSize)
	ss.endpoints[sessionID] = ep
	return ep
}

// GetAudio 获取该会话合成音频的字节流视图
//
// 过渡形态，播放侧改为直接订阅 AudioEndpoint 后删除：字节视图表达不了打断，
// 因此打断事件在这条视图上不可见，由播放侧改订阅端点后自行处理
// resp: 本轮响应，只取其会话 ID
// return: 合成音频 PCM16 小端字节的只读通道，端点关闭后被关闭
func (ss *Service) GetAudio(resp *llm.TurnResponse) <-chan []byte {
	ep := ss.AudioEndpoint(resp.SessionId)

	out := make(chan []byte, defaultAudioBufSize)
	go func() {
		defer close(out)
		for {
			if ev, ok := ep.Next(); ok {
				if ev.Kind == AudioData {
					out <- ev.PCM
				}
				continue
			}
			if ep.Closed() {
				return
			}
			<-ep.Wait()
		}
	}()
	return out
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
