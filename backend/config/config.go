// Package config 统一管理后端所有配置项
//
// 加载优先级：环境变量 > config.yaml
// 先读取 config.yaml 作为默认值，再读取环境变量覆盖同名配置项
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 后端完整配置，各模块通过此 struct 获取各自的配置子项
type Config struct {
	Server   ServerConfig   `yaml:"server"`       // HTTP/HTTPS 服务配置
	SFU      SFUConfig      `yaml:"sfu"`          // WebRTC SFU 媒体引擎配置
	ASR      ASRConfig      `yaml:"asr"`          // 语音识别配置
	VAD      VADConfig      `yaml:"vad"`          // 语音活动检测配置
	LLM      LLMConfig      `yaml:"llm"`          // LLM 服务配置
	TTS      TTSConfig      `yaml:"tts"`          // 语音合成配置
	AI       AIConfig       `yaml:"ai"`           // AI 语音助手配置
	Room     RoomConfig     `yaml:"room"`         // 房间与 WebSocket 配置
	Database DatabaseConfig `yaml:"database"`     // PostgreSQL 持久层配置
	Auth     AuthConfig     `yaml:"auth"`         // 用户系统认证与密码哈希配置
	Log      LogConfig      `yaml:"log"`          // 日志配置
	NATS     NATSConfig     `yaml:"nats"`         // NATS JetStream 一致性骨干配置
	Redis    RedisConfig    `yaml:"redis"`        // 登录限流与在线热状态共享的 Redis 配置
	SpiceDB  SpiceDBConfig  `yaml:"spicedb"`      // SpiceDB(Zanzibar) 对象级授权服务配置
	Outbox   OutboxConfig   `yaml:"outbox"`       // 事务性 Outbox relay 参数
	WT       WTConfig       `yaml:"webtransport"` // WebTransport/QUIC 信令端点配置
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

// WTConfig WebTransport/QUIC 信令端点配置（QUIC 走 UDP，需独立 TLS 证书）
type WTConfig struct {
	// Enabled 是否启用 WebTransport 端点，开发默认开
	Enabled bool `yaml:"enabled"`
	// Addr 监听地址（host:port），QUIC UDP 端口默认 :4433
	Addr string `yaml:"addr"`
	// CertFile QUIC 握手用 TLS 证书文件，浏览器 WebTransport 需要受信 CA
	CertFile string `yaml:"cert_file"`
	// KeyFile 与 CertFile 配套的私钥文件
	KeyFile string `yaml:"key_file"`
}

// AIConfig AI 语音助手配置
type AIConfig struct {
	// StandbyTimeout 在线状态静默超时后自动转待机的时长
	// 使用 Go time.ParseDuration 可解析的格式，单位支持 ns / us(µs) / ms / s / m / h，可组合书写
	// 例如 "500ms"、"60s"、"1m30s"、"2h"
	// 默认 "60s"，可通过环境变量 AI_STANDBY_TIMEOUT 覆盖
	StandbyTimeout string `yaml:"standby_timeout"`
}

// DatabaseConfig PostgreSQL 持久层连接配置
type DatabaseConfig struct {
	Host     string `yaml:"host"`      // PostgreSQL 主机地址，默认 127.0.0.1
	Port     int    `yaml:"port"`      // PostgreSQL 端口，默认 5433（避开生产栈默认 5432）
	User     string `yaml:"user"`      // 连接用户名
	Password string `yaml:"password"`  // 连接密码
	Name     string `yaml:"name"`      // 数据库名
	TimeZone string `yaml:"time_zone"` // 会话时区，默认 Asia/Shanghai
}

// RedisConfig 热状态与限流的共享 Redis 连接配置
type RedisConfig struct {
	// Addr Redis 地址（host:port），默认 127.0.0.1:6379
	Addr string `yaml:"addr"`
	// Password 连接密码，本地开发默认空；生产必须用环境变量覆盖
	Password string `yaml:"password"`
}

// NATSConfig NATS JetStream 一致性骨干配置（事务性 Outbox 事件的投递目的地）
type NATSConfig struct {
	URL            string   `yaml:"url"`             // NATS 地址，默认 127.0.0.1:4222
	Stream         string   `yaml:"stream"`          // JetStream 流名，事件按 subject 落入该流
	StreamSubjects []string `yaml:"stream_subjects"` // 流覆盖的 subject 前缀，如 user.> / room.>
	StreamMaxAge   string   `yaml:"stream_max_age"`  // 事件保留时长，过期由 JetStream 自动回收
}

// SpiceDBConfig 对象级授权服务配置（关系元组写入与权限判定）
type SpiceDBConfig struct {
	// Enabled 是否启用授权服务，开发默认开（dev compose 已编排），生产按需
	Enabled bool `yaml:"enabled"`
	// Addr gRPC 地址（host:port），默认 127.0.0.1:50051
	Addr string `yaml:"addr"`
	// PresharedKey gRPC 预共享密钥，dev 明文传输，生产必须走 TLS
	PresharedKey string `yaml:"preshared_key"`
}

// OutboxConfig 事务性 Outbox relay 参数
type OutboxConfig struct {
	PollInterval string `yaml:"poll_interval"` // relay 轮询 pending 事件的间隔
	BatchSize    int    `yaml:"batch_size"`    // 单批取件上限
	MaxAttempts  int    `yaml:"max_attempts"`  // 单条事件最大投递尝试次数，超限置 failed
}

// DSN 拼接成 pgx 驱动可用的连接串
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.TimeZone)
}

// AuthConfig 用户系统认证与密码哈希配置
type AuthConfig struct {
	JWTSecret       string       `yaml:"jwt_secret"`        // access token 的 HMAC 签名密钥，生产必须用环境变量覆盖
	AccessTokenTTL  string       `yaml:"access_token_ttl"`  // access token 有效期，默认 "15m"
	RefreshTokenTTL string       `yaml:"refresh_token_ttl"` // refresh token 有效期，默认 "720h"（30 天）
	VerifyBaseURL   string       `yaml:"verify_base_url"`   // 前端邮箱验证页 URL 前缀，用于拼接一次性链接
	ResetBaseURL    string       `yaml:"reset_base_url"`    // 前端密码重置页 URL 前缀
	Argon2          Argon2Config `yaml:"argon2"`            // argon2id 密码哈希参数
}

// Argon2Config argon2id 参数，OWASP 推荐内存密集型反 GPU 并行爆破
type Argon2Config struct {
	MemoryKiB   uint32 `yaml:"memory_kib"`  // 内存开销 KiB，默认 65536（64 MiB）
	Iterations  uint32 `yaml:"iterations"`  // 迭代次数，默认 3
	Parallelism uint8  `yaml:"parallelism"` // 并行度，默认 4
	SaltLength  uint32 `yaml:"salt_length"` // 随机盐字节数，默认 16
	KeyLength   uint32 `yaml:"key_length"`  // 派生密钥字节数，默认 32
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
		Database: DatabaseConfig{
			Host:     "127.0.0.1",
			Port:     5433,
			User:     "echat",
			Password: "echat",
			Name:     "echat_dev",
			TimeZone: "Asia/Shanghai",
		},
		Auth: AuthConfig{
			JWTSecret:       "dev-insecure-change-me",
			AccessTokenTTL:  "15m",
			RefreshTokenTTL: "720h",
			VerifyBaseURL:   "http://localhost:5173/verify",
			ResetBaseURL:    "http://localhost:5173/reset",
			Argon2: Argon2Config{
				MemoryKiB:   65536,
				Iterations:  3,
				Parallelism: 4,
				SaltLength:  16,
				KeyLength:   32,
			},
		},
		Log: LogConfig{
			Level:         "info",
			Format:        "console",
			EnableConsole: true,
			EnableFile:    true,
			FileDir:       "logs",
		},
		NATS: NATSConfig{
			URL:            "127.0.0.1:4222",
			Stream:         "echat_events",
			StreamSubjects: []string{"user.>", "room.>"},
			StreamMaxAge:   "168h",
		},
		Redis: RedisConfig{
			Addr:     "127.0.0.1:6379",
			Password: "",
		},
		SpiceDB: SpiceDBConfig{
			Enabled:      true,
			Addr:         "127.0.0.1:50051",
			PresharedKey: "dev-secret-local",
		},
		WT: WTConfig{
			Enabled:  true,
			Addr:     "127.0.0.1:4433",
			CertFile: "certs/dev.crt",
			KeyFile:  "certs/dev.key",
		},
		Outbox: OutboxConfig{
			PollInterval: "1s",
			BatchSize:    16,
			MaxAttempts:  8,
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

	// --- Database ---
	if v := os.Getenv("DB_HOST"); v != "" {
		cfg.Database.Host = v
	}
	if v := os.Getenv("DB_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Database.Port = n
		}
	}
	if v := os.Getenv("DB_USER"); v != "" {
		cfg.Database.User = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		cfg.Database.Password = v
	}
	if v := os.Getenv("DB_NAME"); v != "" {
		cfg.Database.Name = v
	}
	if v := os.Getenv("DB_TIME_ZONE"); v != "" {
		cfg.Database.TimeZone = v
	}

	// --- Auth ---
	if v := os.Getenv("AUTH_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	if v := os.Getenv("AUTH_ACCESS_TTL"); v != "" {
		cfg.Auth.AccessTokenTTL = v
	}
	if v := os.Getenv("AUTH_REFRESH_TTL"); v != "" {
		cfg.Auth.RefreshTokenTTL = v
	}
	if v := os.Getenv("AUTH_VERIFY_BASE_URL"); v != "" {
		cfg.Auth.VerifyBaseURL = v
	}
	if v := os.Getenv("AUTH_RESET_BASE_URL"); v != "" {
		cfg.Auth.ResetBaseURL = v
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

	// --- NATS ---
	if v := os.Getenv("NATS_URL"); v != "" {
		cfg.NATS.URL = v
	}
	if v := os.Getenv("NATS_STREAM"); v != "" {
		cfg.NATS.Stream = v
	}
	if v := os.Getenv("NATS_STREAM_SUBJECTS"); v != "" {
		cfg.NATS.StreamSubjects = strings.Split(v, ",")
	}
	if v := os.Getenv("NATS_STREAM_MAX_AGE"); v != "" {
		cfg.NATS.StreamMaxAge = v
	}

	// --- Redis ---
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}

	// --- SpiceDB ---
	if v := os.Getenv("SPICEDB_ENABLED"); v != "" {
		cfg.SpiceDB.Enabled = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("SPICEDB_GRPC_ADDR"); v != "" {
		cfg.SpiceDB.Addr = v
	}
	if v := os.Getenv("SPICEDB_PRESHARED_KEY"); v != "" {
		cfg.SpiceDB.PresharedKey = v
	}

	// --- Outbox relay ---
	if v := os.Getenv("OUTBOX_POLL_INTERVAL"); v != "" {
		cfg.Outbox.PollInterval = v
	}
	if v := os.Getenv("OUTBOX_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Outbox.BatchSize = n
		}
	}
	if v := os.Getenv("OUTBOX_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Outbox.MaxAttempts = n
		}
	}

	// --- WebTransport/QUIC ---
	if v := os.Getenv("WT_ENABLED"); v != "" {
		cfg.WT.Enabled = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("WT_ADDR"); v != "" {
		cfg.WT.Addr = v
	}
	if v := os.Getenv("WT_CERT_FILE"); v != "" {
		cfg.WT.CertFile = v
	}
	if v := os.Getenv("WT_KEY_FILE"); v != "" {
		cfg.WT.KeyFile = v
	}
	// quic-go 要求 host:port 形式，裸端口写法（"4433"）补齐后再交给监听
	cfg.WT.Addr = normalizeListenAddr(cfg.WT.Addr)
}

// normalizeListenAddr 把监听地址规整成 host:port 形式，兼容两种易写错的写法
// 裸端口 "4433" → ":4433"（quic-go 直接收到会报 address 4433: missing port in address）
// 已是 host:port 的原样返回；IPv6 / 主机名等无法判定的写法保持原样，由 bind 报错给出真实原因
func normalizeListenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return addr
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if _, err := strconv.Atoi(addr); err == nil {
		return ":" + addr
	}
	return addr
}
