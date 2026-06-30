package main

import (
	"echat-backend/asr_cli"
	"echat-backend/config"
	"echat-backend/handlers"
	"echat-backend/room"
	"echat-backend/sfu"
	"log"
	"net/http"
)

// main 加载配置、初始化各模块，然后启动 HTTP/HTTPS 服务
func main() {
	_, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	sfu.InitSFU()
	asr_cli.Init(config.Get().ASR)
	sfu.StartASRLogger()
	//vad_cli.InitVADClient()

	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/ws", handlers.WebSocketHandler)

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
		if err := http.ListenAndServeTLS(addr, cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile, nil); err != nil {
			log.Fatal("https server failed to start: ", err)
		}
		return
	}

	log.Printf("voice room backend starting on http://%s", logAddr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("http server failed to start: ", err)
	}
}
