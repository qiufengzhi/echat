package main

import (
	"context"

	"echat-backend/asr_cli"
	"echat-backend/authn"
	"echat-backend/authz"
	"echat-backend/config"
	"echat-backend/global"
	"echat-backend/handlers"
	"echat-backend/llm_cli"
	"echat-backend/logging"
	"echat-backend/outbox"
	"echat-backend/room"
	"echat-backend/sfu"
	"echat-backend/store"
	"echat-backend/tts_cli"
	"github.com/redis/go-redis/v9"
	"net/http"
	"time"
)

// main 加载配置、初始化各模块，然后启动 HTTP/HTTPS 服务
func main() {
	// 加载配置文件
	_, err := config.Load("config.yaml")
	if err != nil {
		logging.L().Fatalw("加载配置失败", "error", err)
	}

	// 初始化日志系统，必须在 config.Load() 之后
	logging.Init(logging.Config{
		Level:         config.Get().Log.Level,
		Format:        config.Get().Log.Format,
		EnableConsole: config.Get().Log.EnableConsole,
		EnableFile:    config.Get().Log.EnableFile,
		FileDir:       config.Get().Log.FileDir,
	})
	defer logging.Sync() // 确保退出前日志落盘

	asr_cli.Init()       // 初始化 ASR rpc客户端
	sfu.StartASRLogger() // 提取ASR识别结果，并送LLM处理
	//vad_cli.InitVADClient()
	llm_cli.Init() // 初始化 LLM rpc客户端
	tts_cli.Init() // 初始化 TTS

	// 初始化 PostgreSQL 持久层（用户系统地基；连接失败即退出，提示先启动 dev compose）
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()
	st, err := store.Open(dbCtx, config.Get().Database)
	if err != nil {
		logging.L().Fatalw("数据库初始化失败", "error", err,
			"hint", "先运行: docker compose -f deploy/docker-compose.dev.yml up -d")
	}
	defer st.Close()

	// 开发期 schema 迁移（幂等；生产迁移以 backend/migrations 版本化文件为准）
	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err = st.Migrate(migCtx); err != nil {
		logging.L().Fatalw("开发期 schema 迁移失败", "error", err)
	}

	// 认证域：注册 / 邮箱验证 / 登录与会话 / 密码 接口
	// 受保护端点统一走 AccessRequired 中间件（access 签名+有效期+ver 新鲜度校验）
	authSvc := authn.NewService(st.Ent(), config.Get().Auth, authn.ConsoleMailer{}, config.Get().Auth.VerifyBaseURL)
	// 登录限流：Redis 分布式计数优先；Redis 连不上降级进程内实现（限流失效不阻断登录主链路）
	// rdb 为共享客户端，后续在线热状态层（#23 心跳 ZSET）复用同一连接
	rdb := redis.NewClient(&redis.Options{Addr: config.Get().Redis.Addr, Password: config.Get().Redis.Password})
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		logging.L().Warnw("Redis 不可用，登录限流降级为进程内实现", "addr", config.Get().Redis.Addr, "error", err)
	} else {
		authSvc.SetLoginLimiter(authn.NewRedisLoginLimiter(rdb))
		room.SetOnlineStore(rdb) // 在线热状态瞬态层复用同一连接，心跳写入 ZSET
	}
	pingCancel()
	authHandler := authn.NewHandler(authSvc)
	http.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
	http.HandleFunc("POST /api/v1/auth/verify", authHandler.Verify)
	http.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
	http.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)
	http.HandleFunc("POST /api/v1/auth/logout", authHandler.Logout)
	http.HandleFunc("POST /api/v1/auth/logout-all", authHandler.AccessRequired(authHandler.LogoutAll))
	http.HandleFunc("POST /api/v1/auth/email/verify-request", authHandler.AccessRequired(authHandler.EmailBind))
	http.HandleFunc("POST /api/v1/auth/password/reset-request", authHandler.PasswordResetRequest)
	http.HandleFunc("POST /api/v1/auth/password/reset", authHandler.PasswordReset)
	http.HandleFunc("POST /api/v1/auth/password/change", authHandler.AccessRequired(authHandler.PasswordChange))

	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/ws", handlers.WebSocketHandler(authSvc)) // 注册带鉴权的 WebSocket 处理函数

	// 注入 SpiceDB 授权服务：控制面鉴权用（kick/manage_ai/mod_mic/speak 校验、交接），媒体路径永不查
	// 连接失败不阻断启动：授权判定在 room 域逐点接入（#21）时判空降级
	spiceCfg := config.Get().SpiceDB
	if spiceCfg.Enabled {
		authzSvc, err := authz.NewClient(spiceCfg.Addr, spiceCfg.PresharedKey)
		if err != nil {
			logging.L().Warnw("SpiceDB 客户端初始化失败，授权服务降级", "addr", spiceCfg.Addr, "error", err)
		} else {
			// EnsureSchema 幂等（已存在则略过），成功即证明连接与 schema 均就绪，作为挂载判据
			authzCtx, authzCancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err = authzSvc.EnsureSchema(authzCtx); err != nil {
				logging.L().Warnw("SpiceDB schema 水合失败，授权服务降级（room 域鉴权暂跳过）", "addr", spiceCfg.Addr, "error", err)
			} else {
				global.AuthZ = authzSvc
				logging.L().Infow("SpiceDB 授权服务就绪", "addr", spiceCfg.Addr)
			}
			authzCancel()
		}
	}

	// 启动后台清理协程，定期回收空房间
	room.StartCleanupLoop()

	// 启动吊销消费协程：会话被吊销/封禁时踢掉对应实时连接（封禁即下线）
	room.StartRevokedKick()

	// 注入持久层：房间域把 join/leave/切房主/AI 开关的当前态写库并联同事件入 outbox
	room.SetStore(st.Pool())

	// 启动一致性骨干：事务性 Outbox relay 把 pending 事件投递到 NATS JetStream
	// NATS 不可用时事件留在 outbox 堆积，连接恢复后自动补发（业务主链路不受影响）
	streamMaxAge, err := time.ParseDuration(config.Get().NATS.StreamMaxAge)
	if err != nil || streamMaxAge <= 0 {
		streamMaxAge = 7 * 24 * time.Hour
	}
	pollInterval, err := time.ParseDuration(config.Get().Outbox.PollInterval)
	if err != nil || pollInterval <= 0 {
		pollInterval = time.Second
	}
	batchSize := config.Get().Outbox.BatchSize
	if batchSize <= 0 {
		batchSize = 16
	}
	maxAttempts := config.Get().Outbox.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	relayer := outbox.NewRelayer(st.Pool(), outbox.Config{
		NatsURL:        config.Get().NATS.URL,
		Stream:         config.Get().NATS.Stream,
		StreamSubjects: config.Get().NATS.StreamSubjects,
		StreamMaxAge:   streamMaxAge,
		PollInterval:   pollInterval,
		BatchSize:      batchSize,
		MaxAttempts:    maxAttempts,
	})
	go relayer.Run(context.Background())

	// 启动 AI 状态变更广播协程，把唤醒/休眠/静默超时等迁移同步给前端
	room.StartAIStateBroadcaster()

	// 启动在线静默超时清理协程，静默超过阈值自动转待机，时长来自配置 ai.standby_timeout
	standbyTimeout, err := time.ParseDuration(config.Get().AI.StandbyTimeout)
	if err != nil || standbyTimeout <= 0 {
		standbyTimeout = 60 * time.Second // 配置非法时兜底 60 秒
	}
	global.AIStates.StartStandbyCleanup(10*time.Second, standbyTimeout)

	cfg := config.Get()
	addr := cfg.Server.Addr
	logAddr := "localhost" + addr

	logger := logging.New("main")

	if cfg.Server.HTTPSEnabled {
		if cfg.Server.TLSCertFile == "" || cfg.Server.TLSKeyFile == "" {
			logger.Fatalw("TLS_CERT_FILE and TLS_KEY_FILE must be set when HTTPS_ENABLED=true")
		}
		logger.Infow("echat启动成功", "url", "https://"+logAddr)
		if err = http.ListenAndServeTLS(addr, cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile, nil); err != nil {
			logger.Fatalw("https server failed to start", "error", err)
		}
		return
	}

	logger.Infow("echat启动成功", "url", "http://"+logAddr)
	if err = http.ListenAndServe(addr, nil); err != nil {
		logger.Fatalw("http server failed to start", "error", err)
	}
}
