// Package asr 封装阿里云智能语音交互（NLS）实时语音识别能力。
//
// 使用的阿里云 SDK：github.com/aliyun/alibabacloud-nls-go-sdk
// 按 session 维度管理云端识别连接：每来一个新 session_id 就创建一个 SpeechTranscription 实例，
// 音频发送完毕后（is_last=true 或超时）自动关闭该 session 的连接。
//
// 接入方式（参考 asr_cli 的 channel 模式）：
//
//	recognizer := asr.NewAliyunRecognizer(config)
//	go recognizer.Start()
//	recognizer.AudioIn <- audioChunk   // 输入原始 PCM16 小端字节
//	transcript := <-recognizer.AudioOut // 读取识别结果
//
// 所需环境变量（建议）：
//
//	ALIBABA_CLOUD_ACCESS_KEY_ID     阿里云 AccessKey ID
//	ALIBABA_CLOUD_ACCESS_KEY_SECRET 阿里云 AccessKey Secret
//	NLS_APP_KEY                     阿里云智能语音交互项目 AppKey
package asr_cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"echat-backend/config"
	"echat-backend/dataplane"
	asrpb "echat-backend/proto/asr"

	nls "github.com/aliyun/alibabacloud-nls-go-sdk"
)

// resultDropped 累计丢弃的识别结果
//
// 输出汇总通道满时丢弃：消费端繁忙不应把云端 SDK 的回调协程堵住
var resultDropped = dataplane.NewCounter("asr.result", 0)

// DefaultConfig 返回一套推荐默认值，调用方可在此基础上修改。
// 三个 boolean 开关（中间结果 / 标点 / ITN）默认全部开启，符合语音房实时字幕场景。
func DefaultConfig() AliyunASRConfig {
	return AliyunASRConfig{
		URL:                         nls.DEFAULT_URL,
		SampleRate:                  16000,
		EnableIntermediateResult:    true,
		EnablePunctuationPrediction: true,
		EnableITN:                   true,
		MaxSentenceSilence:          800,
		SessionIdleTimeout:          30 * time.Second,
	}
}

// fillDefaults 用环境变量和零值填充补全缺失字段。
// 注意：boolean 字段用零值 false 表示"未设置"，
// 调用方应使用 DefaultConfig() 获得推荐默认值再覆盖。
func (c *AliyunASRConfig) fillDefaults() {
	if c.SampleRate == 0 {
		c.SampleRate = 16000
	}
	if c.MaxSentenceSilence == 0 {
		c.MaxSentenceSilence = 800
	}
	if c.SessionIdleTimeout == 0 {
		c.SessionIdleTimeout = 30 * time.Second
	}
}

// Validate 检查必要参数是否齐全。
func (c *AliyunASRConfig) Validate() error {
	if c.AccessKeyID == "" {
		return errors.New("AliyunASRConfig: AccessKeyID 为空，请设置环境变量 ALIBABA_CLOUD_ACCESS_KEY_ID 或在代码中赋值")
	}
	if c.AccessKeySecret == "" {
		return errors.New("AliyunASRConfig: AccessKeySecret 为空，请设置环境变量 ALIBABA_CLOUD_ACCESS_KEY_SECRET 或在代码中赋值")
	}
	if c.AppKey == "" {
		return errors.New("AliyunASRConfig: AppKey 为空，请设置环境变量 NLS_APP_KEY 或在代码中赋值")
	}
	if c.SampleRate != 8000 && c.SampleRate != 16000 {
		return errors.New("AliyunASRConfig: SampleRate 必须是 8000 或 16000")
	}
	if c.MaxSentenceSilence < 200 || c.MaxSentenceSilence > 2000 {
		return errors.New("AliyunASRConfig: MaxSentenceSilence 范围 200~2000")
	}
	return nil
}

// buildTranscriptionParam 根据 AliyunASRConfig 构造 SDK 参数。
func (s *asrSession) buildTranscriptionParam(cfg *AliyunASRConfig) nls.SpeechTranscriptionStartParam {
	p := nls.DefaultSpeechTranscriptionParam()
	p.Format = nls.PCM
	p.SampleRate = cfg.SampleRate
	p.EnableIntermediateResult = cfg.EnableIntermediateResult
	p.EnablePunctuationPrediction = cfg.EnablePunctuationPrediction
	p.EnableInverseTextNormalization = cfg.EnableITN
	p.MaxSentenceSilence = cfg.MaxSentenceSilence
	return p
}

// start 打开 WebSocket 连接并开始识别。
func (s *asrSession) start(cfg *AliyunASRConfig) error {
	param := s.buildTranscriptionParam(cfg)
	ready, err := s.trans.Start(param, nil)
	if err != nil {
		return err
	}
	// 等待 SDK 握手完成（最多 20 秒）
	select {
	case ok := <-ready:
		if !ok {
			return errors.New("nls Start 返回失败")
		}
	case <-time.After(20 * time.Second):
		return errors.New("nls Start 超时")
	}
	return nil
}

// stop 正常结束识别（会等待服务端返回最终结果）。
func (s *asrSession) stop() {
	trans := s.transcription()
	if trans == nil {
		return // 建连尚未完成，没有可停止的连接
	}
	ready, err := trans.Stop()
	if err != nil {
		logger.Warnw("Stop 错误", "sessionID", s.id, "error", err)
		return
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		logger.Warnw("Stop 等待超时", "sessionID", s.id)
	}
}

// shutdown 强制断开连接。
func (s *asrSession) shutdown() {
	trans := s.transcription()
	if trans == nil {
		return // 建连尚未完成，没有可断开的连接
	}
	trans.Shutdown()
}

// transcription 取本会话的云端识别实例，未建连完成时返回 nil
//
// return: 识别实例，可能为 nil
func (s *asrSession) transcription() *nls.SpeechTranscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trans
}

// GlobalRecognizer 全局 ASR 识别器实例，由 Init() 初始化
// 仅在 provider="aliyun" 且 Init() 成功后非 nil，使用前需判空
var GlobalRecognizer *Recognizer

// Init 从配置文件初始化全局 ASR 识别器并启动
// 必须在 config.Load() 之后调用
func Init() {
	cfg := config.Get().ASR
	switch cfg.Provider {
	case "aliyun":
		initAliyun(cfg)
	case "mock":
		// 压测钩子：mock 识别器按会话定时产出句末结果（docs/loadtest.md §6）
		// 直接复用 mock 的双通道作为门面通道，Push/Results 无需感知差别
		mockRec := startMockLoop(cfg.Mock)
		GlobalRecognizer = &Recognizer{AudioIn: mockRec.AudioIn, AudioOut: mockRec.AudioOut}
		mockActive = mockRec
		logger.Warnw("ASR 已切换为 mock 识别器（压测模式，不发起真实云端识别）", "provider", cfg.Provider)
	default:
		logger.Warnw("ASR 未启用", "provider", cfg.Provider)
	}
}

// initAliyun 初始化阿里云识别器（原 Init 主体）
func initAliyun(cfg config.ASRConfig) {
	aliyunCfg := DefaultConfig()
	if cfg.Aliyun.AccessKeyID != "" {
		aliyunCfg.AccessKeyID = cfg.Aliyun.AccessKeyID
	}
	if cfg.Aliyun.AccessKeySecret != "" {
		aliyunCfg.AccessKeySecret = cfg.Aliyun.AccessKeySecret
	}
	if cfg.Aliyun.AppKey != "" {
		aliyunCfg.AppKey = cfg.Aliyun.AppKey
	}
	aliyunCfg.EnableIntermediateResult = cfg.Aliyun.EnableIntermediate
	aliyunCfg.EnablePunctuationPrediction = cfg.Aliyun.EnablePunctuation
	aliyunCfg.EnableITN = cfg.Aliyun.EnableITN
	if cfg.Aliyun.MaxSentenceSilenceMs > 0 {
		aliyunCfg.MaxSentenceSilence = cfg.Aliyun.MaxSentenceSilenceMs
	}

	var err error
	GlobalRecognizer, err = NewRecognizer(aliyunCfg)
	if err != nil {
		logger.Fatalw("创建阿里云 ASR 识别器失败", "error", err)
	}
	logger.Infow("阿里云 ASR 已初始化")
	go GlobalRecognizer.Start()
}

// NewRecognizer 创建一个阿里云 ASR 识别器
// 调用方负责在不需要时调用 Start()（会阻塞在读取循环），
// 或通过关闭 AudioIn 通道来触发优雅退出
func NewRecognizer(cfg AliyunASRConfig) (*Recognizer, error) {
	cfg.fillDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// SDK 内部日志：静默，不输出到 stderr
	sdkLogger := nls.NewNlsLogger(os.Stderr, "", 0)
	sdkLogger.SetLogSil(true)
	sdkLogger.SetDebug(false)

	r := &Recognizer{
		cfg:       cfg,
		AudioIn:   make(chan *asrpb.AudioChunk, 64),
		AudioOut:  make(chan *asrpb.TranscriptAudioChunk, 64),
		sessions:  make(map[string]*asrSession),
		sdkLogger: sdkLogger,
		outputFan: make(chan sessionResult, 64),
	}
	return r, nil
}

// Start 启动识别器主循环，会阻塞直到 AudioIn 关闭。
// 通常放在一个 goroutine 里运行。
func (r *Recognizer) Start() {
	defer func() {
		// 清理所有剩余 session：先关队列让各自发送协程退出，再断开云端连接
		r.mu.Lock()
		for _, s := range r.sessions {
			s.queue.Close()
			s.shutdown()
		}
		r.sessions = make(map[string]*asrSession)
		r.mu.Unlock()

		close(r.outputFan)
	}()

	// 输出转发协程：从 outputFan 读取 → 写 AudioOut
	go r.forwardResults()

	// 后台清理协程：定期扫描空闲超时的 session
	go r.idleCleaner()

	// 接收协程：只做按会话分派，不做任何阻塞 IO
	//
	// 建连与写云端都在各自的会话发送协程里，因此某个会话的云端变慢或握手耗时
	// 不会拖住其他会话；此前建连就发生在这个循环体内，一次握手会阻塞全站音频识别
	for chunk := range r.AudioIn {
		// session_id 为空时跳过
		if chunk.SessionId == "" {
			logger.Warnw("跳过空 session_id 的音频块")
			continue
		}

		s := r.getOrCreateSession(chunk.SessionId, chunk.RoomId, chunk.ClientId)
		if s == nil {
			continue
		}
		// IsLast 与音频一样入队：让它排在已到达的音频之后，云端收到的顺序与采集顺序一致
		s.queue.Push(chunk)
	}

	// AudioIn 关闭 → 通知外部主循环退出
	logger.Infow("AudioIn 已关闭，退出 Start")
	close(r.AudioOut)
}

// forwardResults 将各个 session 的结果汇总写入 AudioOut。
func (r *Recognizer) forwardResults() {
	for sr := range r.outputFan {
		res := &asrpb.TranscriptAudioChunk{
			SessionId: sr.SessionID,
			RoomId:    sr.RoomID,
			ClientId:  sr.ClientID,
			Text:      sr.Text,
			IsFinal:   sr.IsFinal,
			Seq:       sr.Seq,
		}
		r.AudioOut <- res
	}
}

// idleCleaner 定期检查是否有 session 空闲超时，自动关闭。
func (r *Recognizer) idleCleaner() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			now := time.Now()
			var toClose []string
			for id, s := range r.sessions {
				s.mu.Lock()
				idle := now.Sub(s.lastAudio)
				s.mu.Unlock()
				if idle > r.cfg.SessionIdleTimeout {
					toClose = append(toClose, id)
				}
			}
			r.mu.Unlock()

			for _, id := range toClose {
				logger.Infow("空闲超时，自动关闭", "sessionID", id)
				r.closeSession(id, true) // true = 强制 shutdown
			}
		}
	}
}

// sessionQueueCapacity 每个会话的待发送音频队列容量，50 块 × 20ms = 1 秒音频
const sessionQueueCapacity = 50

// getOrCreateSession 获取或创建指定 session_id 的 asrSession
//
// 只登记会话与其队列，不做任何阻塞 IO：建连由本会话的发送协程负责
func (r *Recognizer) getOrCreateSession(sessionID, roomID, clientID string) *asrSession {
	r.mu.Lock()
	if s, ok := r.sessions[sessionID]; ok {
		r.mu.Unlock()
		return s
	}

	s := &asrSession{
		id:        sessionID,
		roomID:    roomID,
		clientID:  clientID,
		out:       r.outputFan,
		lastAudio: time.Now(),
		queue:     dataplane.NewRing[*asrpb.AudioChunk]("asr.session."+sessionID, sessionQueueCapacity),
	}
	r.sessions[sessionID] = s
	r.mu.Unlock()

	go r.runSession(s)
	return s
}

// runSession 单个会话的发送协程：先建连，再按序把队列里的音频送给云端
//
// 建连（含握手，上限 20 秒）在本协程内完成，不阻塞其他会话；
// 建连期间到达的音频在本会话队列里排队，握手完成后按序补送，首句不再出现空洞
// s: 目标会话
func (r *Recognizer) runSession(s *asrSession) {
	trans, err := r.connect(s)
	if err != nil {
		logger.Warnw("建连失败，放弃该会话", "sessionID", s.id, "error", err)
		r.closeSession(s.id, true)
		return
	}
	logger.Infow("已创建并启动 session", "sessionID", s.id)

	for {
		chunk, ok := s.queue.Pop()
		if !ok {
			if s.queue.Closed() {
				return
			}
			<-s.queue.Wait()
			continue
		}

		if len(chunk.Pcm) > 0 {
			if sendErr := trans.SendAudioData(chunk.Pcm); sendErr != nil {
				logger.Warnw("发送音频失败", "sessionID", s.id, "error", sendErr)
			}
		}

		s.mu.Lock()
		s.lastAudio = time.Now()
		s.mu.Unlock()

		if chunk.IsLast {
			logger.Infow("收到 is_last，关闭 session", "sessionID", s.id)
			r.closeSession(s.id, false)
			return
		}
	}
}

// connect 建立与云端的识别连接并启动识别
//
// 返回的实例交给本会话的发送协程使用，同时写入 s.trans 供关闭路径取用
// s: 目标会话
// return: 识别实例，失败时返回错误
func (r *Recognizer) connect(s *asrSession) (*nls.SpeechTranscription, error) {
	// SDK 内部日志：静默，不输出到 stderr
	sdkSessLogger := nls.NewNlsLogger(os.Stderr, "", 0)
	sdkSessLogger.SetLogSil(true)
	sdkSessLogger.SetDebug(false)
	s.sdkLogger = sdkSessLogger

	cg, err := nls.NewConnectionConfigWithAKInfoDefault(r.cfg.URL, r.cfg.AppKey,
		r.cfg.AccessKeyID, r.cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("创建连接配置失败: %w", err)
	}

	trans, err := nls.NewSpeechTranscription(
		cg, sdkSessLogger,
		func(text string, param interface{}) { s.onTaskFailed(text) },    // 识别过程中的错误处理回调参数
		func(text string, param interface{}) { s.onStarted(text) },       // 建连完成回调参数
		func(text string, param interface{}) { s.onSentenceBegin(text) }, // 一句话开始
		func(text string, param interface{}) { s.onSentenceEnd(text) },   // 一句话结束
		func(text string, param interface{}) { s.onResultChanged(text) }, // 识别中间结果回调参数
		func(text string, param interface{}) { s.onCompleted(text) },     // 识别完成回调参数
		func(param interface{}) { s.onClosed() },                         // 连接断开回调参数
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 SpeechTranscription 失败: %w", err)
	}

	s.mu.Lock()
	s.trans = trans
	s.mu.Unlock()

	// 连接断开时触发的收尾
	s.closeFunc = func() { r.closeSession(s.id, true) }

	if err = s.start(&r.cfg); err != nil {
		return nil, fmt.Errorf("启动识别失败: %w", err)
	}
	return trans, nil
}

// closeSession 关闭指定 session。
// force 为 true 时直接 Shutdown，否则走正常的 Stop() 等待最终结果。
func (r *Recognizer) closeSession(sessionID string, force bool) {
	r.mu.Lock()
	s, ok := r.sessions[sessionID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.sessions, sessionID)
	r.mu.Unlock()

	// 收尾只做一次：正常结束与连接断开回调可能同时到达，重复调用会并发操作同一个云端连接
	s.closeOnce.Do(func() {
		// 先关队列，让本会话的发送协程退出，避免它在会话已清理后继续投递
		s.queue.Close()
		if force {
			s.shutdown()
		} else {
			s.stop()
		}
	})
	logger.Infow("session 已关闭", "sessionID", sessionID, "force", force)
}

// ---------- 回调函数 ----------

// emit 把一条识别结果交给输出汇总通道，永不阻塞
//
// 汇总通道满时丢弃并计数：消费端繁忙不应把云端 SDK 的回调协程堵住，
// 那会连带拖慢整个连接的收发
// result: 待投递的识别结果
func (s *asrSession) emit(result sessionResult) {
	select {
	case s.out <- result:
	default:
		resultDropped.Drop("sessionID", s.id, "text", result.Text)
	}
}

// onTaskFailed 识别过程中的错误回调，JSON 字符串包含错误信息。
func (s *asrSession) onTaskFailed(text string) {
	logger.Warnw("TaskFailed", "sessionID", s.id, "text", text)
	// 将错误信息也作为结果输出，方便前端感知
	s.emit(sessionResult{
		SessionID: s.id,
		RoomID:    s.roomID,
		ClientID:  s.clientID,
		Text:      text,
		IsFinal:   false,
		Seq:       -1,
	})
}

// onStarted 建连完成回调。
func (s *asrSession) onStarted(text string) {
	logger.Infow("连接已建立", "sessionID", s.id, "text", text)
}

// onSentenceBegin 一句话开始回调。
func (s *asrSession) onSentenceBegin(text string) {
	logger.Infow("SentenceBegin", "sessionID", s.id, "text", text)
}

// onSentenceEnd 一句话结束回调，text 是该句话的最终文本。
func (s *asrSession) onSentenceEnd(text string) {
	logger.Infow("SentenceEnd", "sessionID", s.id, "text", text)
	s.emit(sessionResult{
		SessionID: s.id,
		RoomID:    s.roomID,
		ClientID:  s.clientID,
		Text:      text,
		IsFinal:   true,
		Seq:       0, // sentence end 不区分 seq，由前端拼接
	})
}

// onResultChanged 中间结果变化回调，text 是当前识别到的部分文本。
func (s *asrSession) onResultChanged(text string) {
	logger.Debugw("ResultChanged", "sessionID", s.id, "text", text)
}

// onCompleted 识别完成回调。
func (s *asrSession) onCompleted(text string) {
	logger.Infow("识别完成", "sessionID", s.id, "text", text)
}

// onClosed 连接断开回调。
func (s *asrSession) onClosed() {
	logger.Infow("连接已断开", "sessionID", s.id)

	// 关闭 session
	go s.closeFunc()
}
