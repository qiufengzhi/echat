package sfu

import (
	"sync"
	"time"
)

// speakingGrace 最后一帧发出后仍视为「在出声」的时间窗
//
// 帧写入 RTP 后还要经过浏览器 jitter buffer 才真正播完，
// 这段时间物理上无法撤回；窗口取 500ms，覆盖队列排空后的播放尾巴，
// 使「LLM 已结束、音频仍在播」的尾巴窗口也能命中打断
const speakingGrace = 500 * time.Millisecond

// playbackState 单个会话的 AI 音频播放态
//
// 由 SFU 的 pacer 协程在每帧发出时刷新，由识别结果分发侧查询：
// 打断的判定锚点必须是「AI 是否在出声」，而不是「LLM RPC 是否在途」
type playbackState struct {
	// mu 保护下列字段，pacer 每帧写一次、分发侧偶发读，量级极小
	mu sync.Mutex
	// lastSentAt 最近一帧 AI 音频发出的时刻，零值表示尚未发过
	lastSentAt time.Time
	// playedTurn 最近发出的帧所属轮次
	playedTurn string
	// playedSentence 最近发出的帧在该轮内的句子序号（从 1 计），用于播出前缀裁剪
	playedSentence int32
}

// markSent 记录一帧音频已发出
func (p *playbackState) markSent(turn string, sentence int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSentAt = time.Now()
	p.playedTurn = turn
	p.playedSentence = sentence
}

// speaking 返回该会话的 AI 音频是否仍在播出
func (p *playbackState) speaking() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastSentAt.IsZero() {
		return false
	}
	return time.Since(p.lastSentAt) <= speakingGrace
}

// played 返回指定轮次已播到的句子序号；轮次不符时返回 0
//
// 方法名不与字段 playedSentence 重名，返回值按「已播到的句子序号」语义命名
func (p *playbackState) played(turn string) int32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playedTurn != turn {
		return 0
	}
	return p.playedSentence
}

// playbackTable sessionID -> 播放态，随 TTS 循环建立、随循环退出移除
var playbackTable sync.Map

// registerPlayback 登记 TTS 循环的播放态，同一会话重复登记返回原实例
func registerPlayback(sessionID string) *playbackState {
	if v, ok := playbackTable.Load(sessionID); ok {
		return v.(*playbackState)
	}
	ps := &playbackState{}
	actual, _ := playbackTable.LoadOrStore(sessionID, ps)
	return actual.(*playbackState)
}

// unregisterPlayback 移除会话的播放态登记，TTS 循环退出时调用
func unregisterPlayback(sessionID string) {
	playbackTable.Delete(sessionID)
}

// playbackSpeaking 查询会话的 AI 音频是否仍在播出；未登记视为不在播
func playbackSpeaking(sessionID string) bool {
	v, ok := playbackTable.Load(sessionID)
	if !ok {
		return false
	}
	return v.(*playbackState).speaking()
}

// playbackPlayed 查询会话指定轮次已播到的句子序号；未登记或轮次不符返回 0
func playbackPlayed(sessionID, turnID string) int32 {
	v, ok := playbackTable.Load(sessionID)
	if !ok {
		return 0
	}
	return v.(*playbackState).played(turnID)
}
