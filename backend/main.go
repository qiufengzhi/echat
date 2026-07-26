package main

import (
	"echat-backend/asr_cli"
	"echat-backend/config"
	"echat-backend/handlers"
	"echat-backend/llm_cli"
	"echat-backend/logging"
	"echat-backend/room"
	"echat-backend/sfu"
	"echat-backend/tts_cli"
	"net/http"
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

	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/ws", handlers.WebSocketHandler) // 注册 WebSocket 处理函数

	// 启动后台清理协程，定期回收空房间
	room.StartCleanupLoop()

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
