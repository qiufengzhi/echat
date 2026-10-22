// mock 合成器：压测钩子（docs/loadtest.md §6，任务书 3.2 约束的显式配置项）
//
// 仅 tts.provider=mock 时启用，默认配置下零行为变化。设计要点：
//   - 产出静音 PCM，但按时序形态对齐真实合成：首块延迟（TTFB）+ 按句长折算播出时长 +
//     实时节奏分块输出——打断时序、播放窗口与真实链路同量级；
//   - 沿用 Provider 接口与 SessionPipeline 管道语义，service 层零改动。
package tts_cli

import (
	"context"
	"sync"
	"time"

	"echat-backend/config"
	"echat-backend/observability"
)

// MockSynthesizer mock TTS 合成器
type MockSynthesizer struct {
	mu         sync.Mutex
	sessions   map[string]*SessionPipeline
	ttfb       time.Duration // 首块音频前延迟
	msPerChar  int           // 每字符折算的播出毫秒数
	sampleRate int           // 输出采样率，与 config.TTS.SampleRate 一致
}

// initMockTTS 构造 mock 合成器并注册为全局 provider
func initMockTTS(cfg config.TTSConfig) {
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	ttfb := cfg.Mock.TTFBMs
	if ttfb <= 0 {
		ttfb = 150
	}
	mpc := cfg.Mock.MsPerChar
	if mpc <= 0 {
		mpc = 120
	}
	provider = &MockSynthesizer{
		sessions:   make(map[string]*SessionPipeline),
		ttfb:       time.Duration(ttfb) * time.Millisecond,
		msPerChar:  mpc,
		sampleRate: sampleRate,
	}
	logger.Warnw("TTS 已切换为 mock 合成器（压测模式，产出静音音频，不调用真实云端合成）",
		"ttfbMs", ttfb, "msPerChar", mpc)
}

// CreateSession 创建或获取指定 sessionID 的 TTS 管道
func (s *MockSynthesizer) CreateSession(sessionID string) *SessionPipeline {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sp, ok := s.sessions[sessionID]; ok {
		return sp
	}

	ctx, cancel := context.WithCancel(context.Background())
	sp := &SessionPipeline{
		sentenceCh: make(chan taggedSentence, defaultSentenceBufSize),
		audioOutCh: make(chan synthChunk, defaultAudioBufSize),
		ctx:        ctx,
		cancel:     cancel,
		isActive:   true,
	}
	s.sessions[sessionID] = sp
	go s.runSession(sp, sessionID)
	return sp
}

// GetSession 获取指定 sessionID 的管道
func (s *MockSynthesizer) GetSession(sessionID string) (*SessionPipeline, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sessions[sessionID]
	return sp, ok
}

// RemoveSession 移除并取消指定 sessionID 的管道
func (s *MockSynthesizer) RemoveSession(sessionID string) {
	s.mu.Lock()
	sp, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	if ok {
		sp.Cancel()
	}
}

// runSession mock 会话主循环：从 sentenceCh 读取句子，逐句产出静音音频
func (s *MockSynthesizer) runSession(sp *SessionPipeline, sessionID string) {
	defer func() {
		s.RemoveSession(sessionID)
		close(sp.audioOutCh)
	}()

	for {
		select {
		case sent, ok := <-sp.sentenceCh:
			if !ok {
				return
			}
			s.synthesizeSentence(sp, sent, sessionID)
		case <-sp.ctx.Done():
			return
		}
	}
}

// synthesizeSentence 产出单句的静音音频：TTFB 延迟 + 按句长折算时长 + 实时节奏分块
func (s *MockSynthesizer) synthesizeSentence(sp *SessionPipeline, sent taggedSentence, sessionID string) {
	runes := len([]rune(sent.text))
	if runes == 0 {
		return
	}
	observability.AddTTSCharacters("mock", runes)
	observability.AddTTSRequest("mock", "ok")

	// 每块 20ms：与真实音频分块粒度一致，播放侧时序行为对齐
	const chunkMs = 20
	bytesPerChunk := s.sampleRate * 2 * chunkMs / 1000
	chunks := s.msPerChar * runes / chunkMs
	if chunks <= 0 {
		chunks = 1
	}

	select {
	case <-time.After(s.ttfb):
	case <-sp.ctx.Done():
		return
	}

	silence := make([]byte, bytesPerChunk) // 静音：PCM16 全零
	for i := 0; i < chunks; i++ {
		select {
		case sp.audioOutCh <- synthChunk{sentenceSeq: sent.seq, pcm: silence}:
		case <-sp.ctx.Done():
			return
		}
		time.Sleep(chunkMs * time.Millisecond)
	}
}
