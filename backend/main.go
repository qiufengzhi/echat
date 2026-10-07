// main 加载配置、装配根 App、启动后台子系统并监听 HTTP/HTTPS
//
// 仅负责编排：业务装配（store/Redis/认证/路由）已收编进 app.New/app.Mount
// 本文件保留各可选子系统的启动（SFU 日志、SpiceDB、outbox relay、清理协程、WebTransport）
package main

import (
	"context"
	"net/http"
	"time"

	"echat-backend/agentcli"
	"echat-backend/app"
	"echat-backend/asr_cli"
	"echat-backend/authz"
	"echat-backend/config"
	"echat-backend/global"
	"echat-backend/logging"
	"echat-backend/observability"
	"echat-backend/outbox"
	"echat-backend/projection"
	llmpb "echat-backend/proto/llm"
	"echat-backend/room/signaling"
	"echat-backend/sfu"
	"echat-backend/tts_cli"
	"echat-backend/wt"

	"go.opentelemetry.io/otel"
)

// main 组装根进程：配置 → 日志 → App → 后台子系统 → 监听
func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		logging.L().Fatalw("加载配置失败", "error", err)
	}
	// 初始化日志系统，必须在 config.Load() 之后
	logging.Init(logging.Config{
		Level:         cfg.Log.Level,
		Format:        cfg.Log.Format,
		EnableConsole: cfg.Log.EnableConsole,
		EnableFile:    cfg.Log.EnableFile,
		FileDir:       cfg.Log.FileDir,
	})
	defer logging.Sync() // 确保退出前日志落盘

	// 可观测性：OTel 三信号经 OTLP 推向 Collector；失败不阻断启动（观测是增强不是依赖）
	obsShutdown, err := observability.Init(cfg.Observability)
	if err != nil {
		logging.L().Warnw("可观测性初始化失败，继续启动", "error", err)
	} else {
		defer obsShutdown()
	}
	// 启动留痕：一次启动一条 trace，便于把日志/后续轮次追到时间基准上
	_, bootSpan := otel.Tracer("echat-backend").Start(context.Background(), "main.boot")

	// 媒体侧初始化：ASR / ASR 结果分发 / TTS / Agent 客户端
	asr_cli.Init()
	sfu.StartASRDispatcher()
	tts_cli.Init()
	initAgentClient(cfg)

	// 装配根：打开 DB、校验迁移、构造认证服务、Redis 与统一路由树
	root, err := app.New(cfg)
	if err != nil {
		logging.L().Fatalw("装配失败", "error", err)
	}
	defer root.Store().Close()
	root.Mount()

	// 注入 SpiceDB 授权服务：控制面鉴权用（kick/manage_ai/mod_mic/speak 校验、交接）
	// 连接失败不阻断启动：授权判定在 room 域逐点判空降级
	if cfg.SpiceDB.Enabled {
		authzSvc, err := authz.NewClient(cfg.SpiceDB.Addr, cfg.SpiceDB.PresharedKey)
		if err != nil {
			logging.L().Warnw("SpiceDB 客户端初始化失败，授权服务降级", "addr", cfg.SpiceDB.Addr, "error", err)
		} else if err = ensureAuthzSchema(authzSvc); err != nil {
			logging.L().Warnw("SpiceDB schema 水合失败，授权服务降级", "addr", cfg.SpiceDB.Addr, "error", err)
		} else {
			global.AuthZ = authzSvc
			logging.L().Infow("SpiceDB 授权服务就绪", "addr", cfg.SpiceDB.Addr)
		}
	}

	// 注入房间域事实落库与事件记账：join/leave/交接/AI/成员管理写库共用 App 的连接池
	signaling.SetStore(root.Store().Pool())

	// 一致性骨干：事务性 Outbox relay 把 pending 事件投递到 NATS JetStream
	relayer := outbox.NewRelayer(root.Store().Pool(), outboxConfig(cfg))
	go relayer.Run(context.Background())

	// 后台协程：空房间清理 / 吊销踢连接 / 在线待机收敛（AI 状态广播随房间创建启动）
	signaling.StartCleanupLoop()
	signaling.StartRevokedKick()
	startAIStandbyCleanup(cfg)

	// 房间事件投影：durable consumer 消费 room.* 事件，重建读模型并对账 SpiceDB 授权
	go projection.StartRoomReadModel(context.Background(), root.Redis(), root.Store().Pool(), cfg.NATS.URL, cfg.NATS.Stream, global.AuthZ)

	// WebTransport/QUIC 信令端点：握手复用 WebSocket 的 access token 鉴权
	if cfg.WT.Enabled {
		startWebTransport(cfg, root)
	}

	// 启动留痕 span 在进入长生命周期监听前收尾：boot 段 = 准备耗时，监听本身不属于启动
	bootSpan.End()
	serve(cfg, root.Handler())
}

// initAgentClient 初始化每轮一次 RPC 的 Agent 客户端
//
// cfg: 全局配置，取 LLM 段的服务地址、分片数与单轮超时
func initAgentClient(cfg *config.Config) {
	err := agentcli.Init(agentcli.Options{
		Addr:        cfg.LLM.GrpcAddr,
		Shards:      cfg.LLM.Shards,
		TurnTimeout: time.Duration(cfg.LLM.TurnTimeoutSec) * time.Second,
		Dispatch: func(resp *llmpb.TurnResponse) {
			// 该房间 AI 已离线时不播放回复，待机/在线则正常播放
			if global.AIStates.Get(resp.RoomId) == global.AIOffline {
				return
			}
			tts_cli.GlobalTTSService.ProcessText(resp)
		},
	})
	if err != nil {
		logging.L().Fatalw("初始化 Agent 客户端失败", "error", err)
	}
}

// ensureAuthzSchema 幂等写入 SpiceDB schema，成功即证明连接与 schema 均就绪
func ensureAuthzSchema(svc *authz.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return svc.EnsureSchema(ctx)
}

// outboxConfig 组装 outbox relay 参数，非法时长/批次回退默认值
func outboxConfig(cfg *config.Config) outbox.Config {
	streamMaxAge, err := time.ParseDuration(cfg.NATS.StreamMaxAge)
	if err != nil || streamMaxAge <= 0 {
		streamMaxAge = 7 * 24 * time.Hour
	}
	pollInterval, err := time.ParseDuration(cfg.Outbox.PollInterval)
	if err != nil || pollInterval <= 0 {
		pollInterval = time.Second
	}
	batchSize := cfg.Outbox.BatchSize
	if batchSize <= 0 {
		batchSize = 16
	}
	maxAttempts := cfg.Outbox.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	return outbox.Config{
		NatsURL:        cfg.NATS.URL,
		Stream:         cfg.NATS.Stream,
		StreamSubjects: cfg.NATS.StreamSubjects,
		StreamMaxAge:   streamMaxAge,
		PollInterval:   pollInterval,
		BatchSize:      batchSize,
		MaxAttempts:    maxAttempts,
	}
}

// startAIStandbyCleanup 启动在线静默超时清理协程，超时阈值来自 ai.standby_timeout
func startAIStandbyCleanup(cfg *config.Config) {
	standbyTimeout, err := time.ParseDuration(cfg.AI.StandbyTimeout)
	if err != nil || standbyTimeout <= 0 {
		standbyTimeout = 60 * time.Second // 配置非法时兜底 60 秒
	}
	global.AIStates.StartStandbyCleanup(10*time.Second, standbyTimeout)
}

// startWebTransport 启动 QUIC 信令端点，失败仅告警（前端自动降级 WebSocket）
func startWebTransport(cfg *config.Config, root *app.App) {
	wtSrv, err := wt.NewServer(cfg.WT, root.AuthService())
	if err != nil {
		logging.L().Warnw("WebTransport 端点初始化失败，前端将降级 WebSocket", "error", err)
		return
	}
	go func() {
		if err := wtSrv.ListenAndServe(); err != nil {
			logging.L().Warnw("WebTransport 服务退出，前端将降级 WebSocket", "addr", cfg.WT.Addr, "error", err)
		}
	}()
	logging.L().Infow("WebTransport 服务启动中", "addr", cfg.WT.Addr)
}

// serve 按配置以 HTTPS 或 HTTP 启动监听，绑定到 App 的根 handler
func serve(cfg *config.Config, handler http.Handler) {
	logAddr := "localhost" + cfg.Server.Addr
	logger := logging.New("main")
	if cfg.Server.HTTPSEnabled {
		if cfg.Server.TLSCertFile == "" || cfg.Server.TLSKeyFile == "" {
			logger.Fatalw("TLS_CERT_FILE and TLS_KEY_FILE must be set when HTTPS_ENABLED=true")
		}
		logger.Infow("echat启动成功", "url", "https://"+logAddr)
		if err := http.ListenAndServeTLS(cfg.Server.Addr, cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile, handler); err != nil {
			logger.Fatalw("https server failed to start", "error", err)
		}
		return
	}
	logger.Infow("echat启动成功", "url", "http://"+logAddr)
	if err := http.ListenAndServe(cfg.Server.Addr, handler); err != nil {
		logger.Fatalw("http server failed to start", "error", err)
	}
}
