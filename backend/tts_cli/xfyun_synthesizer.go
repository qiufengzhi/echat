package tts_cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"echat-backend/config"
)

// ---------- 讯飞 TTS 常量 ----------

const (
	xfyunWSAddr = "wss://tts-api.xfyun.cn/v2/tts" // 讯飞 TTS WebSocket 地址
	xfyunHost   = "tts-api.xfyun.cn"              // 签名用 host
)

// ---------- 讯飞 TTS 配置 ----------

// XfyunTTSConfig 讯飞语音合成配置
type XfyunTTSConfig struct {
	AppID      string // 讯飞控制台 APPID（必填）
	APIKey     string // 讯飞 APIKey（必填）
	APISecret  string // 讯飞 APISecret（必填）
	Voice      string // 发音人，默认 "xiaoyan"
	Speed      int    // 语速 0-100，默认 50
	Volume     int    // 音量 0-100，默认 50
	Pitch      int    // 音高 0-100，默认 50
	SampleRate int    // 采样率，8000 或 16000，默认 16000
	AudioFmt   string // 音频编码: raw(pcm) / lame(mp3) / opus / opus-wb，默认 raw
}

// applyDefaults 填充默认值
func (c *XfyunTTSConfig) applyDefaults() {
	if c.Voice == "" {
		c.Voice = "xiaoyan"
	}
	if c.Speed < 0 || c.Speed > 100 {
		c.Speed = 50
	}
	if c.Volume < 0 || c.Volume > 100 {
		c.Volume = 50
	}
	if c.Pitch < 0 || c.Pitch > 100 {
		c.Pitch = 50
	}
	if c.SampleRate == 0 {
		c.SampleRate = 16000
	}
	if c.AudioFmt == "" {
		c.AudioFmt = "raw"
	}
}

// auf 返回采样率参数字符串
func (c *XfyunTTSConfig) auf() string {
	return fmt.Sprintf("audio/L16;rate=%d", c.SampleRate)
}

// ---------- WebSocket 消息结构 ----------

// ---------- 讯飞 WebSocket 消息结构 ----------
//
// 以下结构体对应讯飞 TTS v2 API 的 JSON 协议字段
// 参考文档: https://www.xfyun.cn/doc/tts/online_tts/API.html

// xfRequest 发送给讯飞 TTS 的合成请求
//
// 通过 WebSocket TextMessage 发送，一次请求合成单句文本
// 讯飞收到后会逐帧返回音频（xfResponse）
// status=1 表示中间音频块，status=2 表示合成结束
type xfRequest struct {
	Common   xfCommon   `json:"common"`   // 通用参数，含 APPID
	Business xfBusiness `json:"business"` // 业务参数：发音人、语速、音量、编码等
	Data     xfData     `json:"data"`     // 数据体：文本内容与传输状态
}

// xfCommon 讯飞请求通用参数
type xfCommon struct {
	AppID string `json:"app_id"` // 讯飞控制台应用 APPID，用于鉴权识别
}

// xfBusiness 讯飞请求业务参数
//
// 控制合成音色、格式和效果
// 字段名来自讯飞协议的参数简写，非标准完整单词
type xfBusiness struct {
	Aue    string `json:"aue"`              // 音频编码: raw(pcm) / lame(mp3) / opus / opus-wb
	Auf    string `json:"auf,omitempty"`    // 采样率描述，如 "audio/L16;rate=16000"
	Vcn    string `json:"vcn"`              // 发音人（voice name），如 "xiaoyan"
	Speed  int    `json:"speed,omitempty"`  // 语速，0-100，默认 50
	Volume int    `json:"volume,omitempty"` // 音量，0-100，默认 50
	Pitch  int    `json:"pitch,omitempty"`  // 音高，0-100，默认 50
	Tte    string `json:"tte,omitempty"`    // 文本编码方式，固定 "UTF8"
}

// xfData 讯飞请求数据体
//
// 当前实现采用"一次性传输"模式：将完整文本 Base64 编码后
// status 固定为 2 一次性发送，不走分片
type xfData struct {
	Status int    `json:"status"` // 传输状态：2=一次性全部文本，分片模式下 1=有后续 2=结束
	Text   string `json:"text"`   // Base64 编码的待合成文本
}

// xfResponse 讯飞返回的单帧 JSON 响应
//
// 每个 WebSocket 帧都是独立的 JSON，Code != 0 表示业务错误
// Code == 0 时 Data 中携带音频块或结束信号
type xfResponse struct {
	Code    int         `json:"code"`           // 业务状态码，0=正常
	Message string      `json:"message"`        // 错误信息，Code != 0 时有值
	SID     string      `json:"sid,omitempty"`  // 讯飞合成会话 ID，用于问题排查
	Data    *xfRespData `json:"data,omitempty"` // 音频数据与合成状态
}

// xfRespData 讯飞响应数据体
//
// Audio 字段按 Base64 编码返回原始音频片段，解码后直接累加即可
// Status 区分中间帧（1=合成中）和结束帧（2=合成完成）
// 客户端收到 Status=2 后可关闭连接或发送下一句
type xfRespData struct {
	Audio  string `json:"audio"`  // Base64 编码的 PCM 音频片段
	CED    string `json:"ced"`    // Cursor of Encoded Data，已合成文本字节偏移量
	Status int    `json:"status"` // 合成状态：1=合成中（有后续音频），2=合成结束
}

// ---------- 讯飞 TTS 合成器 ----------

// XfyunSynthesizer 讯飞 TTS 合成器，实现 Provider 接口
//
// 通过 WebSocket 调用讯飞在线语音合成 API
// 每句文本建立一次 WebSocket 连接，完成即关闭
// 与 AliyunSynthesizer 完全独立，可单独启用
type XfyunSynthesizer struct {
	cfg      XfyunTTSConfig              // 讯飞配置
	sessions map[string]*SessionPipeline // sessionID -> 管道
	mu       sync.Mutex                  // 保护 sessions map
}

// initXfyun 从配置构建讯飞合成器并设为全局 Provider
func initXfyun(cfg config.TTSConfig) {
	xfCfg := XfyunTTSConfig{
		AppID:      cfg.Xfyun.AppID,
		APIKey:     cfg.Xfyun.APIKey,
		APISecret:  cfg.Xfyun.APISecret,
		Voice:      cfg.Xfyun.Voice,
		Speed:      cfg.Xfyun.Speed,
		Volume:     cfg.Xfyun.Volume,
		Pitch:      cfg.Xfyun.Pitch,
		SampleRate: cfg.SampleRate,
		AudioFmt:   cfg.Xfyun.AudioFmt,
	}
	xfCfg.applyDefaults()

	var err error
	provider, err = NewXfyunSynthesizer(xfCfg)
	if err != nil {
		logger.Fatalw("创建讯飞 TTS 合成器失败", "error", err)
	}
	logger.Infow("讯飞 TTS 已初始化",
		"voice", xfCfg.Voice, "speed", xfCfg.Speed, "volume", xfCfg.Volume,
		"pitch", xfCfg.Pitch, "sampleRate", xfCfg.SampleRate, "audioFmt", xfCfg.AudioFmt,
	)
}

// NewXfyunSynthesizer 创建一个讯飞 TTS 合成器
func NewXfyunSynthesizer(cfg XfyunTTSConfig) (*XfyunSynthesizer, error) {
	if cfg.AppID == "" || cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, fmt.Errorf("XfyunTTSConfig: AppID, APIKey, APISecret 不能为空")
	}
	cfg.applyDefaults()

	return &XfyunSynthesizer{
		cfg:      cfg,
		sessions: make(map[string]*SessionPipeline),
	}, nil
}

// CreateSession 创建或获取指定 sessionID 的 TTS 管道
func (s *XfyunSynthesizer) CreateSession(sessionID string) *SessionPipeline {
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
func (s *XfyunSynthesizer) GetSession(sessionID string) (*SessionPipeline, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.sessions[sessionID]
	return sp, ok
}

// RemoveSession 移除并取消指定 sessionID 的管道
func (s *XfyunSynthesizer) RemoveSession(sessionID string) {
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

// runSession 讯飞会话主循环：从 sentenceCh 读取句子，逐个合成
func (s *XfyunSynthesizer) runSession(sp *SessionPipeline, sessionID string) {
	defer func() {
		s.RemoveSession(sessionID)
		close(sp.audioOutCh)
	}()

	for {
		select {
		case sentence, ok := <-sp.sentenceCh:
			if !ok {
				logger.Infow("句子通道已关闭", "sessionID", sessionID)
				return
			}
			s.synthesizeSentence(sp, sentence, sessionID)
		case <-sp.ctx.Done():
			logger.Infow("会话已取消", "sessionID", sessionID)
			return
		}
	}
}

// synthesizeSentence 调用讯飞 TTS WebSocket API 合成单句
//
// 每句文本建立一次独立的 WebSocket 连接，原因是讯飞协议中 status=2 表示一次性传输全部文本，
// 服务端合成完毕后会主动关闭连接，协议不支持在同一连接上发送多个独立文本请求。
// 因此一句文本 = 一条 WebSocket 连接 = 一次完整合成生命周期
// 发送合成请求后流式读取音频块写入管道
func (s *XfyunSynthesizer) synthesizeSentence(sp *SessionPipeline, sentence string, sessionID string) {
	sentence = strings.TrimSpace(sentence)
	// 清理非法 UTF-8 字节：LLM 流式输出可能把多字节字符（如中文）拆在两个 gRPC 消息里，
	// 导致首尾出现无效字节，讯飞收到无法合成，产出噪音
	sentence = strings.ToValidUTF8(sentence, "")
	if sentence == "" {
		return
	}

	logger.Infow("开始合成句子", "sessionID", sessionID, "sentence", sentence)

	// 构建鉴权 URL
	wsURL, err := s.buildAuthURL()
	if err != nil {
		logger.Warnw("构建鉴权 URL 失败", "sessionID", sessionID, "error", err)
		return
	}

	// 建立 WebSocket 连接
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(sp.ctx, wsURL, nil)
	if err != nil {
		logger.Warnw("WebSocket 连接失败", "sessionID", sessionID, "error", err)
		return
	}
	defer conn.Close()

	// 构建并发送合成请求
	reqJSON, err := s.buildRequest(sentence)
	if err != nil {
		logger.Warnw("构建请求失败", "sessionID", sessionID, "error", err)
		return
	}
	logger.Debugw("发送请求 JSON", "sessionID", sessionID, "json", string(reqJSON))
	if err = conn.WriteMessage(websocket.TextMessage, reqJSON); err != nil {
		logger.Warnw("发送请求失败", "sessionID", sessionID, "error", err)
		return
	}

	logger.Infow("已发送合成请求，等待音频返回", "sessionID", sessionID)

	// 接收循环：读取 JSON 帧，解析音频块写入管道
	totalBytes := 0
	for {
		select {
		case <-sp.ctx.Done():
			logger.Infow("合成被取消", "sessionID", sessionID)
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) ||
				strings.Contains(err.Error(), "close 1000") {
				logger.Infow("合成完成", "sessionID", sessionID, "totalBytes", totalBytes)
				return
			}
			logger.Warnw("读取响应失败", "sessionID", sessionID, "error", err)
			return
		}

		var resp xfResponse
		if err = json.Unmarshal(message, &resp); err != nil {
			logger.Warnw("解析响应失败", "sessionID", sessionID, "error", err, "raw", string(message))
			return
		}

		if resp.Code != 0 {
			logger.Warnw("业务错误", "sessionID", sessionID, "code", resp.Code, "message", resp.Message, "sid", resp.SID)
			return
		}

		if resp.Data == nil {
			logger.Debugw("响应 data 为空", "sessionID", sessionID, "raw", string(message))
			continue
		}

		// 解码音频块
		if resp.Data.Audio != "" {
			audio, err := base64.StdEncoding.DecodeString(resp.Data.Audio)
			if err != nil {
				logger.Warnw("解码音频失败", "sessionID", sessionID, "error", err)
				return
			}
			if len(audio) > 0 {
				totalBytes += len(audio)
				select {
				case sp.audioOutCh <- audio:
				case <-sp.ctx.Done():
					logger.Infow("合成被取消", "sessionID", sessionID)
					return
				}
			}
		} else {
			logger.Debugw("响应audio为空", "sessionID", sessionID, "status", resp.Data.Status, "ced", resp.Data.CED)
		}

		// status=2 合成结束
		if resp.Data.Status == 2 {
			logger.Infow("合成完成", "sessionID", sessionID, "totalBytes", totalBytes)
			return
		}
	}
}

// buildAuthURL 构建带 HMAC-SHA256 签名的 WebSocket URL
//
// 讯飞鉴权规则：使用 HMAC-SHA256 对 host + date + request-line 签名
// 拼接到 authorization 参数中
// 参考文档: https://www.xfyun.cn/doc/tts/online_tts/API.html
func (s *XfyunSynthesizer) buildAuthURL() (string, error) {
	date := time.Now().UTC().Format(time.RFC1123)

	// 签名原文（3 行，严格格式）
	signOrigin := fmt.Sprintf("host: %s\ndate: %s\nGET /v2/tts HTTP/1.1", xfyunHost, date)

	// HMAC-SHA256 签名
	mac := hmac.New(sha256.New, []byte(s.cfg.APISecret))
	mac.Write([]byte(signOrigin))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// 构建 authorization 参数
	authOrigin := fmt.Sprintf(
		`api_key="%s", algorithm="hmac-sha256", headers="host date request-line", signature="%s"`,
		s.cfg.APIKey, signature,
	)
	authorization := base64.StdEncoding.EncodeToString([]byte(authOrigin))

	// 拼接最终 URL
	u, err := url.Parse(xfyunWSAddr)
	if err != nil {
		return "", fmt.Errorf("解析讯飞 WS 地址失败: %w", err)
	}
	q := u.Query()
	q.Set("authorization", authorization)
	q.Set("date", date)
	q.Set("host", xfyunHost)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// buildRequest 构建讯飞合成 JSON 请求体
func (s *XfyunSynthesizer) buildRequest(text string) ([]byte, error) {
	encodedText := base64.StdEncoding.EncodeToString([]byte(text))

	req := xfRequest{
		Common: xfCommon{
			AppID: s.cfg.AppID,
		},
		Business: xfBusiness{
			Aue:    s.cfg.AudioFmt,
			Auf:    s.cfg.auf(),
			Vcn:    s.cfg.Voice,
			Speed:  s.cfg.Speed,
			Volume: s.cfg.Volume,
			Pitch:  s.cfg.Pitch,
			Tte:    "utf8",
		},
		Data: xfData{
			Status: 2, // 一次性传输全部文本
			Text:   encodedText,
		},
	}

	return json.Marshal(req)
}
