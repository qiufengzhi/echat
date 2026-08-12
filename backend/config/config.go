// Package config 统一管理后端所有配置项
//
// 加载优先级：环境变量 > config.yaml
// 先读取 config.yaml 作为默认值，再读取环境变量覆盖同名配置项
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 后端完整配置，各模块通过此 struct 获取各自的配置子项
type Config struct {
	Server ServerConfig `yaml:"server"` // HTTP/HTTPS 服务配置
	SFU    SFUConfig    `yaml:"sfu"`    // WebRTC SFU 媒体引擎配置
	ASR    ASRConfig    `yaml:"asr"`    // 语音识别配置
	VAD    VADConfig    `yaml:"vad"`    // 语音活动检测配置
	LLM    LLMConfig    `yaml:"llm"`    // LLM 服务配置
	TTS    TTSConfig    `yaml:"tts"`    // 语音合成配置
	AI     AIConfig     `yaml:"ai"`     // AI 语音助手配置
	Room   RoomConfig   `yaml:"room"`   // 房间与 WebSocket 配置
	Log    LogConfig    `yaml:"log"`    // 日志配置
}

// ServerConfig HTTP/HTTPS 服务配置
type ServerConfig struct {
	Addr         string `yaml:"addr"`          // 监听地址，默认 ":8080"
	HTTPSEnabled bool   `yaml:"https_enabled"` // 是否启用 HTTPS
	TLSCertFile  string `yaml:"tls_cert_file"` // HTTPS 证书文件路径
	TLSKeyFile   string `yaml:"tls_key_file"`  // HTTPS 私钥文件路径
}

// SFUConfig WebRTC SFU 媒体引擎配置
type SFUConfig struct {
	MediaMinPort uint16   `yaml:"media_min_port"` // UDP 端口范围下限，需与 Docker 端口映射一致
	MediaMaxPort uint16   `yaml:"media_max_port"` // UDP 端口范围上限
	NAT1To1IP    string   `yaml:"nat_1_to_1_ip"`  // NAT 公网 IP，逗号分隔多个
	STUNServers  []string `yaml:"stun_servers"`   // ICE STUN 服务器列表
}

// ASRConfig 语音识别配置
type ASRConfig struct {
	Provider string     `yaml:"provider"`  // 识别提供商："aliyun" | ""（禁用）
	Aliyun   AliyunConf `yaml:"aliyun"`    // 阿里云 NLS 配置
	GrpcAddr string     `yaml:"grpc_addr"` // gRPC ASR 地址（预留）
}

// AliyunConf 阿里云智能语音交互配置
type AliyunConf struct {
	AccessKeyID          string `yaml:"access_key_id"`              // 阿里云 AccessKey ID
	AccessKeySecret      string `yaml:"access_key_secret"`          // 阿里云 AccessKey Secret
	AppKey               string `yaml:"app_key"`                    // NLS 项目 AppKey
	EnableIntermediate   bool   `yaml:"enable_intermediate_result"` // 是否返回中间结果
	EnablePunctuation    bool   `yaml:"enable_punctuation"`         // 是否启用标点预测
	EnableITN            bool   `yaml:"enable_itn"`                 // 是否启用中文数字转阿拉伯数字
	MaxSentenceSilenceMs int    `yaml:"max_sentence_silence_ms"`    // 断句静音阈值（毫秒）
	SessionIdleTimeout   string `yaml:"session_idle_timeout"`       // session 空闲超时，默认 "30s"
}

// VADConfig VAD 语音活动检测配置
type VADConfig struct {
	GrpcAddr string `yaml:"grpc_addr"` // VAD gRPC 服务地址
}

// LLMConfig LLM 服务配置
type LLMConfig struct {
	GrpcAddr string `yaml:"grpc_addr"` // LLM gRPC 服务地址
}

// TTSConfig 语音合成配置
type TTSConfig struct {
	Provider   string     `yaml:"provider"`    // 合成提供商："aliyun" | "xfyun" | ""（禁用）
	Aliyun     AliyunConf `yaml:"aliyun"`      // 阿里云语音合成配置（复用 ASR 的阿里云配置）
	Xfyun      XfyunConf  `yaml:"xfyun"`       // 讯飞语音合成配置
	Voice      string     `yaml:"voice"`       // 语音音色，默认 "Alicia"
	SampleRate int        `yaml:"sample_rate"` // 采样率，默认 16000
}

// XfyunConf 讯飞语音合成配置
type XfyunConf struct {
	AppID     string `yaml:"app_id"`     // 讯飞控制台 APPID
	APIKey    string `yaml:"api_key"`    // 讯飞 APIKey
	APISecret string `yaml:"api_secret"` // 讯飞 APISecret
	Voice     string `yaml:"voice"`      // 发音人，如 "x4_xiaoyan"，默认 "xiaoyan"
	Speed     int    `yaml:"speed"`      // 语速 0-100，默认 50
	Volume    int    `yaml:"volume"`     // 音量 0-100，默认 50
	Pitch     int    `yaml:"pitch"`      // 音高 0-100，默认 50
	AudioFmt  string `yaml:"audio_fmt"`  // 音频编码: raw(pcm) / lame(mp3) / opus / opus-wb，默认 raw
}

// RoomConfig 房间与 WebSocket 配置
type RoomConfig struct {
	IdleTimeout   string `yaml:"idle_timeout"`    // 空房间清理间隔
	WSReadBuffer  int    `yaml:"ws_read_buffer"`  // WebSocket 读缓冲区大小（字节）
	WSWriteBuffer int    `yaml:"ws_write_buffer"` // WebSocket 写缓冲区大小（字节）
	WSCheckOrigin bool   `yaml:"ws_check_origin"` // 是否校验 WebSocket 来源
}

// AIConfig AI 语音助手配置
type AIConfig struct {
	// StandbyTimeout 在线状态静默超时后自动转待机的时长
	// 使用 Go time.ParseDuration 可解析的格式，单位支持 ns / us(µs) / ms / s / m / h，可组合书写
	// 例如 "500ms"、"60s"、"1m30s"、"2h"
	// 默认 "60s"，可通过环境变量 AI_STANDBY_TIMEOUT 覆盖
	StandbyTimeout string `yaml:"standby_timeout"`
}

// LogConfig 日志配置
//
// Level 控制输出级别，只输出 >= 该级别的日志：
//
//	debug - 调试信息：ICE candidate、SDP 内容、TTS 字节序诊断、中间识别结果等，仅开发时用
//	info  - 正常业务流程：连接/断连、房间创建销毁、识别结果、合成完成等，生产推荐
//	warn  - 异常但可恢复：编码失败、网络超时、慢客户端消息丢弃等
//	error - 仅严重错误（当前项目暂未使用）
//
// Format 控制输出格式：
//
//	console - 开发模式：彩色文本、带调用位置（文件:行号），人类友好
//	json   - 生产模式：JSON 格式输出，便于日志采集/检索/聚合
//
// EnableConsole 控制是否输出到终端 stderr，默认 true
// EnableFile 控制是否输出到按天日志文件，默认 true
// FileDir 日志文件目录，默认 "logs"，每天一个 echat-YYYY-MM-DD.log，保留 30 天
type LogConfig struct {
	Level         string `yaml:"level"`          // 日志级别：debug | info | warn | error，默认 "info"
	Format        string `yaml:"format"`         // 输出格式：console | json，默认 "console"
	EnableConsole bool   `yaml:"enable_console"` // 输出到终端 stderr，默认 true
	EnableFile    bool   `yaml:"enable_file"`    // 输出到按天日志文件，默认 true
	FileDir       string `yaml:"file_dir"`       // 日志文件目录，默认 "logs"
}

// ---------- 全局实例 ----------

var globalCfg *Config // Load() 成功后存储，供 Get() 取用

// Get 返回已加载的全局配置。必须在 config.Load() 之后再调用，否则返回 nil
func Get() *Config {
	return globalCfg
}

// ---------- 默认值 ----------

// DefaultConfig 返回带开发默认值的 Config 实例，提供开箱即用的本地开发体验
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:         ":8080",
			HTTPSEnabled: false,
		},
		SFU: SFUConfig{
			MediaMinPort: 50000,
			MediaMaxPort: 50100,
			STUNServers:  []string{"stun:stun.l.google.com:19302"},
		},
		ASR: ASRConfig{
			Provider: "aliyun",
			Aliyun: AliyunConf{
				EnableIntermediate:   true,
				EnablePunctuation:    true,
				EnableITN:            true,
				MaxSentenceSilenceMs: 800,
				SessionIdleTimeout:   "30s",
			},
		},
		VAD: VADConfig{
			GrpcAddr: "127.0.0.1:50052",
		},
		LLM: LLMConfig{
			GrpcAddr: "127.0.0.1:50053",
		},
		TTS: TTSConfig{
			Provider:   "aliyun",
			Voice:      "Alicia",
			SampleRate: 16000,
			Xfyun: XfyunConf{
				Voice:    "xiaoyan",
				Speed:    50,
				Volume:   50,
				Pitch:    50,
				AudioFmt: "raw",
			},
		},
		Room: RoomConfig{
			IdleTimeout:   "5m",
			WSReadBuffer:  1024,
			WSWriteBuffer: 1024,
			WSCheckOrigin: true,
		},
		AI: AIConfig{
			StandbyTimeout: "60s",
		},
		Log: LogConfig{
			Level:         "info",
			Format:        "console",
			EnableConsole: true,
			EnableFile:    true,
			FileDir:       "logs",
		},
	}
}

// ---------- 加载 ----------

// Load 从 YAML 文件加载配置，再用环境变量覆盖
// 文件不存在时不报错，使用 DefaultConfig + 环境变量覆盖（适合本地开发只设环境变量不写文件）
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			return loadDone(cfg)
		}
		return nil, fmt.Errorf("读取配置文件 %s: %w", path, err)
	}

	if err = yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}

	applyEnvOverrides(cfg)
	globalCfg = cfg
	return cfg, nil
}

// loadDone 供文件不存在分支复用
func loadDone(cfg *Config) (*Config, error) {
	globalCfg = cfg
	return cfg, nil
}

// applyEnvOverrides 用环境变量覆盖 YAML 中的对应字段
// 环境变量名与 docker-compose 中已有名称保持一致，方便平滑迁移
func applyEnvOverrides(cfg *Config) {
	// --- Server ---
	if v := os.Getenv("SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("HTTPS_ENABLED"); v != "" {
		cfg.Server.HTTPSEnabled = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("TLS_CERT_FILE"); v != "" {
		cfg.Server.TLSCertFile = v
	}
	if v := os.Getenv("TLS_KEY_FILE"); v != "" {
		cfg.Server.TLSKeyFile = v
	}

	// --- SFU ---
	if v := os.Getenv("SFU_MEDIA_MIN_PORT"); v != "" {
		if p, err := strconv.ParseUint(v, 10, 16); err == nil {
			cfg.SFU.MediaMinPort = uint16(p)
		}
	}
	if v := os.Getenv("SFU_MEDIA_MAX_PORT"); v != "" {
		if p, err := strconv.ParseUint(v, 10, 16); err == nil {
			cfg.SFU.MediaMaxPort = uint16(p)
		}
	}
	if v := os.Getenv("SFU_NAT1_TO_1_IP"); v != "" {
		cfg.SFU.NAT1To1IP = v
	}
	if v := os.Getenv("SFU_STUN_SERVERS"); v != "" {
		// 逗号分隔的 STUN 地址列表，如 "stun:stun1.l.google.com:19302,stun:stun2.l.google.com:19302"
		cfg.SFU.STUNServers = strings.Split(v, ",")
		for i := range cfg.SFU.STUNServers {
			cfg.SFU.STUNServers[i] = strings.TrimSpace(cfg.SFU.STUNServers[i])
		}
	}

	// --- ASR ---
	if v := os.Getenv("ASR_PROVIDER"); v != "" {
		cfg.ASR.Provider = v
	}
	if v := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID"); v != "" {
		cfg.ASR.Aliyun.AccessKeyID = v
	}
	if v := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET"); v != "" {
		cfg.ASR.Aliyun.AccessKeySecret = v
	}
	if v := os.Getenv("NLS_APP_KEY"); v != "" {
		cfg.ASR.Aliyun.AppKey = v
	}

	// --- VAD ---
	if v := os.Getenv("VAD_GRPC_ADDR"); v != "" {
		cfg.VAD.GrpcAddr = v
	}

	// --- LLM ---
	if v := os.Getenv("LLM_GRPC_ADDR"); v != "" {
		cfg.LLM.GrpcAddr = v
	}

	// --- TTS ---
	if v := os.Getenv("TTS_PROVIDER"); v != "" {
		cfg.TTS.Provider = v
	}
	if v := os.Getenv("TTS_VOICE"); v != "" {
		cfg.TTS.Voice = v
	}
	if v := os.Getenv("TTS_SAMPLE_RATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TTS.SampleRate = n
		}
	}
	// 讯飞 TTS
	if v := os.Getenv("XF_TTS_APP_ID"); v != "" {
		cfg.TTS.Xfyun.AppID = v
	}
	if v := os.Getenv("XF_TTS_API_KEY"); v != "" {
		cfg.TTS.Xfyun.APIKey = v
	}
	if v := os.Getenv("XF_TTS_API_SECRET"); v != "" {
		cfg.TTS.Xfyun.APISecret = v
	}
	if v := os.Getenv("XF_TTS_VOICE"); v != "" {
		cfg.TTS.Xfyun.Voice = v
	}
	if v := os.Getenv("XF_TTS_SPEED"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TTS.Xfyun.Speed = n
		}
	}
	if v := os.Getenv("XF_TTS_VOLUME"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TTS.Xfyun.Volume = n
		}
	}
	if v := os.Getenv("XF_TTS_PITCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TTS.Xfyun.Pitch = n
		}
	}
	if v := os.Getenv("XF_TTS_AUDIO_FMT"); v != "" {
		cfg.TTS.Xfyun.AudioFmt = v
	}

	// --- Room ---
	if v := os.Getenv("ROOM_IDLE_TIMEOUT"); v != "" {
		cfg.Room.IdleTimeout = v
	}
	if v := os.Getenv("ROOM_WS_READ_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Room.WSReadBuffer = n
		}
	}
	if v := os.Getenv("ROOM_WS_WRITE_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Room.WSWriteBuffer = n
		}
	}
	if v := os.Getenv("ROOM_WS_CHECK_ORIGIN"); v != "" {
		cfg.Room.WSCheckOrigin = strings.EqualFold(v, "true")
	}

	// --- AI ---
	if v := os.Getenv("AI_STANDBY_TIMEOUT"); v != "" {
		cfg.AI.StandbyTimeout = v
	}

	// --- Log ---
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("LOG_ENABLE_CONSOLE"); v != "" {
		cfg.Log.EnableConsole = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("LOG_ENABLE_FILE"); v != "" {
		cfg.Log.EnableFile = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("LOG_FILE_DIR"); v != "" {
		cfg.Log.FileDir = v
	}
}
