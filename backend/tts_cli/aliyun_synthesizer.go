package tts_cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"echat-backend/config"
)

// ---------- 阿里云 TTS 配置 ----------

// AliyunTTSConfig 阿里云语音合成配置
type AliyunTTSConfig struct {
	AccessKeyID     string // 阿里云 AccessKey ID
	AccessKeySecret string // 阿里云 AccessKey Secret
	AppKey          string // 阿里云智能语音交互项目 AppKey
	Voice           string // 语音音色，如 "Alicia"（女声）、"Zhiyang"（男声）
	SampleRate      int    // 采样率，支持 8000、16000
}

// DefaultAliyunConfig 返回阿里云 TTS 默认配置
func DefaultAliyunConfig() AliyunTTSConfig {
	return AliyunTTSConfig{
		Voice:      "Alicia",
		SampleRate: 16000,
	}
}

// ---------- 阿里云 TTS 合成器 ----------

// AliyunSynthesizer 阿里云 TTS 合成器，实现 Provider 接口
//
// 通过 HTTP 调用阿里云流式语音合成 API
// 管理多个会话的独立合成管道
type AliyunSynthesizer struct {
	cfg      AliyunTTSConfig             // 阿里云配置
	sessions map[string]*SessionPipeline // sessionID -> 管道
	mu       sync.Mutex                  // 保护 sessions map
	client   *http.Client                // HTTP 客户端，用于调用阿里云 API
}

// initAliyun 从配置构建阿里云合成器并设为全局 Provider
//
// 凭证优先级：TTS 专用配置 > ASR 配置（TTS 与 ASR 通常共用一个阿里云账号）
func initAliyun(cfg config.TTSConfig) {
	aliyunCfg := DefaultAliyunConfig()
	aliyunCfg.AccessKeyID = cfg.Aliyun.AccessKeyID
	aliyunCfg.AccessKeySecret = cfg.Aliyun.AccessKeySecret
	aliyunCfg.AppKey = cfg.Aliyun.AppKey
	if cfg.Voice != "" {
		aliyunCfg.Voice = cfg.Voice
	}
	if cfg.SampleRate > 0 {
		aliyunCfg.SampleRate = cfg.SampleRate
	}

	var err error
	provider, err = NewAliyunSynthesizer(aliyunCfg)
	if err != nil {
		logger.Fatalw("创建阿里云 TTS 合成器失败", "error", err)
	}
	logger.Infow("阿里云 TTS 已初始化", "voice", aliyunCfg.Voice, "sampleRate", aliyunCfg.SampleRate)
}

// NewAliyunSynthesizer 创建一个阿里云 TTS 合成器
func NewAliyunSynthesizer(cfg AliyunTTSConfig) (*AliyunSynthesizer, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" || cfg.AppKey == "" {
		return nil, fmt.Errorf("AccessKeyID, AccessKeySecret, AppKey 不能为空")
	}

	return &AliyunSynthesizer{
		cfg:      cfg,
		sessions: make(map[string]*SessionPipeline),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// CreateSession 创建或获取指定 sessionID 的 TTS 管道
func (s *AliyunSynthesizer) CreateSession(sessionID string) *SessionPipeline {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sp, ok := s.sessions[sessionID]; ok {
		return sp
	}

	ctx, cancel := context.WithCancel(context.Background())
	sp := &SessionPipeline{
		sentenceCh: make(chan string, defaultSentenceBufSize),
		audioOutCh: make(chan []byte, defaultAudioBufSize),
		ctx:        ctx,
		cancel:     cancel,
		isActive:   true,
	}

	s.sessions[sessionID] = sp
	go s.runSession(sp, sessionID)

	logger.Infow("创建会话管道", "sessionID", sessionID)
	return sp
}

// GetSession 获取指定 sessionID 的管道
func (s *AliyunSynthesizer) GetSession(sessionID string) (*SessionPipeline, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sessions[sessionID]
	return sp, ok
}

// RemoveSession 移除并取消指定 sessionID 的管道
func (s *AliyunSynthesizer) RemoveSession(sessionID string) {
	s.mu.Lock()
	sp, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	if ok {
		sp.Cancel()
		logger.Infow("移除会话管道", "sessionID", sessionID)
	}
}

// runSession 阿里云会话主循环：从 sentenceCh 读取句子，逐个合成
func (s *AliyunSynthesizer) runSession(sp *SessionPipeline, sessionID string) {
	defer func() {
		s.RemoveSession(sessionID)
		close(sp.audioOutCh)
	}()

	var sentenceSeq int // 句子序号，用于跨日志关联音频块归属
	for {
		select {
		case sentence, ok := <-sp.sentenceCh:
			if !ok {
				logger.Infow("句子通道已关闭", "sessionID", sessionID, "totalSentences", sentenceSeq)
				return
			}
			sentenceSeq++
			logger.Infow("开始合成句子", "sessionID", sessionID, "seq", sentenceSeq, "sentence", sentence)
			s.synthesizeSentence(sp, sentence, sessionID, sentenceSeq)
		case <-sp.ctx.Done():
			logger.Infow("会话已取消", "sessionID", sessionID)
			return
		}
	}
}

// synthesizeSentence 调用阿里云 TTS HTTP API 合成单句
//
// 阿里云语音合成 API 文档：https://help.aliyun.com/zh/ai/developer-reference/streaming-speech-synthesis-api
func (s *AliyunSynthesizer) synthesizeSentence(sp *SessionPipeline, sentence string, sessionID string, sentenceSeq int) {
	sentence = strings.TrimSpace(sentence)
	if sentence == "" {
		return
	}

	logger.Debugw("阿里云合成开始", "sessionID", sessionID, "seq", sentenceSeq, "sentence", sentence)

	query := url.Values{}
	query.Set("appkey", s.cfg.AppKey)
	query.Set("text", sentence)
	query.Set("voice", s.cfg.Voice)
	query.Set("format", "pcm")
	query.Set("sample_rate", strconv.Itoa(s.cfg.SampleRate))
	query.Set("timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	query.Set("signature", s.sign(query))

	req, err := http.NewRequestWithContext(sp.ctx, "POST",
		"http://nls-gateway.cn-shanghai.aliyuncs.com/stream/v1/tts",
		strings.NewReader(query.Encode()),
	)
	if err != nil {
		logger.Warnw("创建请求失败", "sessionID", sessionID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	logger.Infow("发送请求到阿里云 TTS API", "sessionID", sessionID)
	resp, err := s.client.Do(req)
	if err != nil {
		logger.Warnw("请求失败", "sessionID", sessionID, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Warnw("API 返回错误", "sessionID", sessionID, "status", resp.Status, "body", string(body))
		return
	}

	logger.Infow("API 返回成功，开始读取流式音频", "sessionID", sessionID)

	buf := make([]byte, 4096)
	totalBytes := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += n
			data := make([]byte, n)
			copy(data, buf[:n])

			select {
			case sp.audioOutCh <- data:
			case <-sp.ctx.Done():
				logger.Infow("合成被取消", "sessionID", sessionID, "bytesRead", totalBytes)
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Warnw("读取音频失败", "sessionID", sessionID, "error", err)
			break
		}
	}

	logger.Infow("合成完成", "sessionID", sessionID, "seq", sentenceSeq, "totalBytes", totalBytes)
}

// sign 生成阿里云 API 签名
// 签名规则：将所有参数按 key 排序后拼接，用 HMAC-SHA256 签名，base64 编码
func (s *AliyunSynthesizer) sign(query url.Values) string {
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(query.Get(k))
	}

	h := hmac.New(sha256.New, []byte(s.cfg.AccessKeySecret))
	h.Write([]byte(sb.String()))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
