package main

import (
	"echat-backend/asr_cli"
	"echat-backend/config"
	"echat-backend/handlers"
	"echat-backend/llm_cli"
	"echat-backend/room"
	"echat-backend/sfu"
	"echat-backend/tts_cli"
	"log"
	"net/http"
)

// main 加载配置、初始化各模块，然后启动 HTTP/HTTPS 服务
func main() {
	// 加载配置文件
	_, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	sfu.InitSFU()        // 初始化 SFU
	asr_cli.Init()       // 初始化 ASR rpc客户端
	sfu.StartASRLogger() // 启动后台协程，持续从 ASR 识别器读取识别结果并打印日志
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

	if cfg.Server.HTTPSEnabled {
		if cfg.Server.TLSCertFile == "" || cfg.Server.TLSKeyFile == "" {
			log.Fatal("TLS_CERT_FILE and TLS_KEY_FILE must be set when HTTPS_ENABLED=true")
		}
		log.Printf("voice room backend starting on https://%s", logAddr)
		if err = http.ListenAndServeTLS(addr, cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile, nil); err != nil {
			log.Fatal("https server failed to start: ", err)
		}
		return
	}

	log.Printf("voice room backend starting on http://%s", logAddr)
	if err = http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("http server failed to start: ", err)
	}
}
