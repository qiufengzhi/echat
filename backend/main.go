package main

import (
	"context"

	"echat-backend/asr_cli"
	"echat-backend/authn"
	"echat-backend/config"
	"echat-backend/global"
	"echat-backend/handlers"
	"echat-backend/llm_cli"
	"echat-backend/logging"
	"echat-backend/room"
	"echat-backend/sfu"
	"echat-backend/store"
	"echat-backend/tts_cli"
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

	// 认证域：注册 / 邮箱验证 接口
	authSvc := authn.NewService(st.Ent(), config.Get().Auth, authn.ConsoleMailer{}, config.Get().Auth.VerifyBaseURL)
	authHandler := authn.NewHandler(authSvc)
	http.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
	http.HandleFunc("POST /api/v1/auth/verify", authHandler.Verify)

	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/ws", handlers.WebSocketHandler) // 注册 WebSocket 处理函数

	// 启动后台清理协程，定期回收空房间
	room.StartCleanupLoop()

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
