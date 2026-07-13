// Package tts 语音合成模块
//
// provider.go 定义 TTS 提供商共用接口、会话管道和全局初始化入口
// 各提供商（阿里云、讯飞等）各自独立实现 Provider 接口
//
// 当前支持的提供商
//   - aliyun: 阿里云流式语音合成（HTTP API），实现在 aliyun_synthesizer.go
//   - xfyun:  讯飞在线语音合成（WebSocket API），实现在 xfyun_synthesizer.go
//   - service: TTS 服务入口，负责断句分发和打断处理，实现在 service.go
package tts_cli

import (
	"context"
	"log"
	"sync"

	"echat-backend/config"
)

// ---------- Provider 接口 ----------

// Provider 语音合成提供商接口
//
// 每个提供商各自实现该接口，管理自己的会话管道
// service 层通过 provider 间接调用
type Provider interface {
	// CreateSession 创建或获取指定 sessionID 的 TTS 管道
	CreateSession(sessionID string) *SessionPipeline

	// GetSession 获取指定 sessionID 的管道，不存在时返回 false
	GetSession(sessionID string) (*SessionPipeline, bool)

	// RemoveSession 移除并取消指定 sessionID 的管道
	RemoveSession(sessionID string)
}

// provider 全局 TTS 提供商实例，由 Init() 按配置初始化，仅包内使用
// 外部包通过 GetProvider() 获取
var provider Provider

// ---------- SessionPipeline 会话管道 ----------

const (
	defaultSentenceBufSize = 32 // 句子缓冲大小：容纳断句器提前产出的句子，填满后丢弃而非阻塞
	defaultAudioBufSize    = 32 // 音频缓冲大小：容纳合成中的音频块，避免播放卡顿
)

// SessionPipeline 每个会话的独立 TTS 管道
// 包含句子输入通道、音频输出通道和取消上下文
// 由各 Provider 的 CreateSession 创建，由 RemoveSession 销毁
type SessionPipeline struct {
	sentenceCh chan string        // 输入：断句后的完整句子，缓冲通道，非阻塞写入
	audioOutCh chan []byte        // 输出：TTS 合成的 PCM 音频块，缓冲通道
	ctx        context.Context    // 用于取消正在进行的合成请求
	cancel     context.CancelFunc // 取消函数
	isActive   bool               // 管道是否活跃
	mu         sync.Mutex         // 保护 isActive
}

// SendSentence 向管道发送一句待合成文本
//
// 非阻塞：队列满时返回 false，调用方自行决定丢弃或重试
func (sp *SessionPipeline) SendSentence(sentence string) bool {
	select {
	case sp.sentenceCh <- sentence:
		return true
	default:
		return false
	}
}

// AudioCh 返回音频输出通道，供调用方读取合成后的 PCM 音频块
func (sp *SessionPipeline) AudioCh() <-chan []byte {
	return sp.audioOutCh
}

// Cancel 取消该会话的所有合成任务
// 中断正在进行的 API 请求，关闭通道。幂等，多次调用无害
func (sp *SessionPipeline) Cancel() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if !sp.isActive {
		return
	}

	sp.isActive = false
	sp.cancel()

	// 排空 sentenceCh，避免发送方永久阻塞
	select {
	case <-sp.sentenceCh:
	default:
	}
}

// IsActive 返回管道是否活跃
func (sp *SessionPipeline) IsActive() bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.isActive
}

// ---------- Init 根据配置初始化对应提供商 ----------

// Init 从配置文件初始化全局 TTS 提供商
//
// 根据 tts.provider 配置选择初始化阿里云或讯飞合成器
// 两者完全独立，互不影响。必须在 config.Load() 之后调用
func Init() {
	cfg := config.Get().TTS

	switch cfg.Provider {
	case "aliyun":
		initAliyun(cfg)
	case "xfyun":
		initXfyun(cfg)
	default:
		// provider 为空或未知值，禁用 TTS
		log.Printf("[tts] 未启用 (provider=%q)，支持 aliyun / xfyun", cfg.Provider)
	}
}
