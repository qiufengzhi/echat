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
	"log"
	"os"
	"time"

	"echat-backend/config"
	asrpb "echat-backend/proto/asr"

	nls "github.com/aliyun/alibabacloud-nls-go-sdk"
)

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
	ready, err := s.trans.Stop()
	if err != nil {
		s.logger.Printf("[asr][%s] Stop 错误: %v", s.id, err)
		return
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		s.logger.Printf("[asr][%s] Stop 等待超时", s.id)
	}
}

// shutdown 强制断开连接。
func (s *asrSession) shutdown() {
	s.trans.Shutdown()
}

// GlobalRecognizer 全局 ASR 识别器实例，由 Init() 初始化
// 仅在 provider="aliyun" 且 Init() 成功后非 nil，使用前需判空
var GlobalRecognizer *Recognizer

// Init 从配置文件初始化全局 ASR 识别器并启动
// 必须在 config.Load() 之后调用
func Init() {
	cfg := config.Get().ASR
	if cfg.Provider != "aliyun" {
		log.Printf("[asr] 未启用 (provider=%q)", cfg.Provider)
		return
	}

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
		log.Fatalf("创建阿里云 ASR 识别器失败: %v", err)
	}
	log.Printf("[asr] 阿里云 ASR 已初始化")
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

	r := &Recognizer{
		cfg:       cfg,
		AudioIn:   make(chan asrpb.AudioChunk, 64),
		AudioOut:  make(chan *asrpb.TranscriptAudioChunk, 64),
		sessions:  make(map[string]*asrSession),
		stopCh:    make(chan struct{}),
		logger:    nls.NewNlsLogger(os.Stderr, "[aliyun-asr] ", log.LstdFlags|log.Lmicroseconds),
		outputFan: make(chan sessionResult, 64),
	}
	r.logger.SetLogSil(true) // 禁止 SDK 输出日志到 stderr
	r.logger.SetDebug(false) // 关闭 debug 级别日志
	return r, nil
}

// Start 启动识别器主循环，会阻塞直到 AudioIn 关闭。
// 通常放在一个 goroutine 里运行。
func (r *Recognizer) Start() {
	defer func() {
		// 清理所有剩余 session
		r.mu.Lock()
		for _, s := range r.sessions {
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

	for chunk := range r.AudioIn {
		// session_id 为空时跳过
		if chunk.SessionId == "" {
			r.logger.Println("[asr] 跳过空 session_id 的音频块")
			continue
		}

		// 获取或创建 session
		s := r.getOrCreateSession(chunk.SessionId, chunk.RoomId, chunk.ClientId)
		if s == nil {
			continue
		}

		// 发送 PCM 数据
		if len(chunk.Pcm) > 0 {
			if err := s.trans.SendAudioData(chunk.Pcm); err != nil {
				r.logger.Printf("[asr][%s] 发送音频失败: %v", chunk.SessionId, err)
			}
		}

		// 更新最后活跃时间
		s.mu.Lock()
		s.lastAudio = time.Now()
		s.mu.Unlock()

		// is_last 时关闭该 session
		if chunk.IsLast {
			r.logger.Printf("[asr][%s] 收到 is_last，关闭 session", chunk.SessionId)
			r.closeSession(chunk.SessionId, false) // false = 正常 stop，等待最终结果
		}
	}

	// AudioIn 关闭 → 通知外部主循环退出
	r.logger.Println("[asr] AudioIn 已关闭，退出 Start")
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
				r.logger.Printf("[asr][%s] 空闲超时，自动关闭", id)
				r.closeSession(id, true) // true = 强制 shutdown
			}
		case <-r.stopCh:
			return
		}
	}
}

// getOrCreateSession 获取或创建指定 session_id 的 asrSession。
func (r *Recognizer) getOrCreateSession(sessionID, roomID, clientID string) *asrSession {
	r.mu.Lock()
	s, ok := r.sessions[sessionID]
	if ok {
		r.mu.Unlock()
		return s
	}
	r.mu.Unlock()

	// 创建新 session
	s = &asrSession{
		id:        sessionID,
		roomID:    roomID,
		clientID:  clientID,
		out:       r.outputFan,
		lastAudio: time.Now(),
	}
	s.logger = nls.NewNlsLogger(os.Stderr, "[aliyun-asr]["+sessionID+"] ", log.LstdFlags|log.Lmicroseconds)
	s.logger.SetLogSil(true) // 禁止 SDK 输出日志到 stderr
	s.logger.SetDebug(false) // 关闭 debug 级别日志

	// 创建 SDK SpeechTranscription 实例
	config, err := nls.NewConnectionConfigWithAKInfoDefault(r.cfg.URL, r.cfg.AppKey,
		r.cfg.AccessKeyID, r.cfg.AccessKeySecret)
	if err != nil {
		r.logger.Printf("[asr][%s] 创建连接配置失败: %v", sessionID, err)
		return nil
	}

	trans, err := nls.NewSpeechTranscription(
		config, s.logger,
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
		r.logger.Printf("[asr][%s] 创建 SpeechTranscription 失败: %v", sessionID, err)
		return nil
	}
	s.trans = trans

	// 启动识别
	if err = s.start(&r.cfg); err != nil {
		r.logger.Printf("[asr][%s] 启动识别失败: %v", sessionID, err)
		return nil
	}

	r.logger.Printf("[asr][%s] 已创建并启动 session", sessionID)

	// 注册到 map
	r.mu.Lock()
	// 双重检查：可能另一个 goroutine 同时创建了
	if existing, ok := r.sessions[sessionID]; ok {
		r.mu.Unlock()
		s.shutdown()
		return existing
	}
	r.sessions[sessionID] = s
	r.mu.Unlock()

	return s
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

	if force {
		s.shutdown()
	} else {
		s.stop()
	}
	r.logger.Printf("[asr][%s] session 已关闭 (force=%v)", sessionID, force)
}

// ---------- 回调函数 ----------

// onTaskFailed 识别过程中的错误回调，JSON 字符串包含错误信息。
func (s *asrSession) onTaskFailed(text string) {
	s.logger.Printf("[asr][%s] TaskFailed: %s", s.id, text)
	// 将错误信息也作为结果输出，方便前端感知
	s.out <- sessionResult{
		SessionID: s.id,
		RoomID:    s.roomID,
		ClientID:  s.clientID,
		Text:      text,
		IsFinal:   false,
		Seq:       -1,
	}
}

// onStarted 建连完成回调。
func (s *asrSession) onStarted(text string) {
	s.logger.Printf("[asr][%s] 连接已建立: %s", s.id, text)
}

// onSentenceBegin 一句话开始回调。
func (s *asrSession) onSentenceBegin(text string) {
	s.logger.Printf("[asr][%s] SentenceBegin: %s", s.id, text)
}

// onSentenceEnd 一句话结束回调，text 是该句话的最终文本。
func (s *asrSession) onSentenceEnd(text string) {
	s.logger.Printf("[asr][%s] SentenceEnd: %s", s.id, text)
	s.out <- sessionResult{
		SessionID: s.id,
		RoomID:    s.roomID,
		ClientID:  s.clientID,
		Text:      text,
		IsFinal:   true,
		Seq:       0, // sentence end 不区分 seq，由前端拼接
	}
}

// onResultChanged 中间结果变化回调，text 是当前识别到的部分文本。
func (s *asrSession) onResultChanged(text string) {
	s.logger.Printf("[asr][%s] ResultChanged: %s", s.id, text)
	//s.out <- sessionResult{
	//	SessionID: s.id,
	//	RoomID:    s.roomID,
	//	ClientID:  s.clientID,
	//	Text:      text,
	//	IsFinal:   false,
	//	Seq:       0,
	//}
}

// onCompleted 识别完成回调。
func (s *asrSession) onCompleted(text string) {
	s.logger.Printf("[asr][%s] 识别完成: %s", s.id, text)
}

// onClosed 连接断开回调。
func (s *asrSession) onClosed() {
	s.logger.Printf("[asr][%s] 连接已断开", s.id)
}
