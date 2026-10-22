// mock 识别器：压测钩子（docs/loadtest.md §6，任务书 3.2 约束的显式配置项）
//
// 仅 asr.provider=mock 时启用，默认配置下零行为变化。设计要点：
//   - 不消费音频内容：按会话定时产出句末识别结果，驱动「唤醒网关 → 打断判定 → 投递 LLM」
//     的完整真实链路，只有云端识别本身是假的；
//   - 会话登记有两条来源：sfu.AcceptOffer 的压测钩子（NotifySession，不依赖音频解码），
//     以及音频块首次到达（ensureSession，与真实路径对齐）；
//   - 结束来源：Leave 推送的 IsLast 标记，与真实路径一致。
package asr_cli

import (
	"bufio"
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"echat-backend/config"
	asrpb "echat-backend/proto/asr"
)

// mockActive 非 nil 表示当前运行在 mock 识别器上，NotifySession 才有效
var mockActive *mockRecognizer

// defaultMockSentries 未配置语料文件时的内置句子（均为普通闲聊句，不命中唤醒/休眠词）
var defaultMockSentences = []string{
	"你好呀，今天过得怎么样。",
	"最近在忙什么呢。",
	"周末有什么打算吗。",
	"今天天气真不错。",
	"晚饭吃了什么。",
}

// mockRecognizer mock 识别器：对外契约与 Recognizer 相同（AudioIn/AudioOut 双通道），
// 内部按会话维护定时产出协程
type mockRecognizer struct {
	// AudioIn 输入通道：接收音频块，仅用于会话生命周期（IsLast 结束会话），内容被忽略
	AudioIn chan *asrpb.AudioChunk
	// AudioOut 输出通道：句末识别结果，供 asrdispatch 消费
	AudioOut chan *asrpb.TranscriptAudioChunk

	firstDelay time.Duration // 会话登记到第一句结果的延迟
	interval   time.Duration // 相邻两句结果的间隔
	texts      []string      // 语料句子，按会话内序号循环取用

	mu       sync.Mutex
	sessions map[string]*mockSession
}

// mockSession 单个 mock 会话的产出协程控制块
type mockSession struct {
	roomID  string
	clientID string
	seq     int64           // 会话内结果序号，从 1 计
	cancel  context.CancelFunc
}

// startMockLoop 构造 mock 识别器并启动其读取循环
//
// 复用 Recognizer 的通道契约：GlobalRecognizer 持有通道，门面函数（Push/Results）
// 无需感知 mock 与真实的差别
func startMockLoop(cfg config.ASRMockConf) *mockRecognizer {
	m := &mockRecognizer{
		AudioIn:    make(chan *asrpb.AudioChunk, 64),
		AudioOut:   make(chan *asrpb.TranscriptAudioChunk, 64),
		firstDelay: time.Duration(cfg.FirstDelayMs) * time.Millisecond,
		interval:   time.Duration(cfg.IntervalMs) * time.Millisecond,
		texts:      loadMockCorpus(cfg.Corpus),
		sessions:   make(map[string]*mockSession),
	}
	if m.firstDelay <= 0 {
		m.firstDelay = time.Second
	}
	if m.interval <= 0 {
		m.interval = 4 * time.Second
	}
	go m.readLoop()
	return m
}

// loadMockCorpus 读取语料文件（每行一句）；文件缺失或为空时回退内置句子
func loadMockCorpus(path string) []string {
	if path == "" {
		return defaultMockSentences
	}
	f, err := os.Open(path)
	if err != nil {
		logger.Warnw("mock ASR 语料文件不可读，回退内置句子", "path", path, "error", err)
		return defaultMockSentences
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		logger.Warnw("mock ASR 语料文件为空，回退内置句子", "path", path)
		return defaultMockSentences
	}
	logger.Infow("mock ASR 语料已加载", "path", path, "lines", len(lines))
	return lines
}

// readLoop 消费音频块：只关心会话生命周期（新会话登记 / IsLast 结束），音频内容忽略
func (m *mockRecognizer) readLoop() {
	for chunk := range m.AudioIn {
		if chunk.SessionId == "" {
			continue
		}
		if chunk.IsLast {
			m.removeSession(chunk.SessionId)
			continue
		}
		m.ensureSession(chunk.SessionId, chunk.RoomId, chunk.ClientId)
	}
}

// ensureSession 登记会话并启动定时产出协程；已存在时为空操作
func (m *mockRecognizer) ensureSession(sessionID, roomID, clientID string) {
	m.mu.Lock()
	if _, ok := m.sessions[sessionID]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &mockSession{roomID: roomID, clientID: clientID, cancel: cancel}
	m.sessions[sessionID] = s
	m.mu.Unlock()

	go m.emitLoop(sessionID, s, ctx)
}

// emitLoop 会话产出协程：firstDelay 后每 interval 产出一条句末识别结果，直到会话结束
func (m *mockRecognizer) emitLoop(sessionID string, s *mockSession, ctx context.Context) {
	logger.Infow("mock ASR 会话开始", "sessionID", sessionID, "roomID", s.roomID, "interval", m.interval)
	timer := time.NewTimer(m.firstDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		m.mu.Lock()
		s.seq++
		text := m.texts[int((s.seq-1)%int64(len(m.texts)))]
		seq := s.seq
		m.mu.Unlock()

		res := &asrpb.TranscriptAudioChunk{
			SessionId: sessionID,
			RoomId:    s.roomID,
			ClientId:  s.clientID,
			Text:      text,
			IsFinal:   true,
			Seq:       seq,
		}
		select {
		case m.AudioOut <- res:
			logger.Debugw("mock ASR 产出句末结果", "sessionID", sessionID, "seq", seq, "text", text)
		case <-ctx.Done():
			return
		}
		timer.Reset(m.interval)
	}
}

// removeSession 结束会话并停止其产出协程
func (m *mockRecognizer) removeSession(sessionID string) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if ok {
		s.cancel()
		logger.Infow("mock ASR 会话结束", "sessionID", sessionID)
	}
}

// NotifySession 压测钩子：sfu 在轨协商时登记 mock 会话，不依赖音频解码路径
//
// 非 mock 模式（mockActive 为 nil）下为空操作——生产路径零影响
func NotifySession(sessionID, roomID, clientID string) {
	if mockActive == nil || sessionID == "" {
		return
	}
	mockActive.ensureSession(sessionID, roomID, clientID)
}
