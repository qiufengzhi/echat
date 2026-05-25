package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"voice-room-backend/handlers"
	"voice-room-backend/room"
)

// main 注册 HTTP 路由，并根据环境变量决定以 HTTP 还是 HTTPS 启动后端服务。
func main() {
	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/ws", handlers.WebSocketHandler)

	// 启动后台清理协程，定期回收可能残留的空房间。
	room.StartCleanupLoop()

	addr := getEnv("SERVER_ADDR", ":8080")
	// 通过环境变量切换本地开发时的 HTTP/HTTPS 模式，避免反复改代码。
	httpsEnabled := strings.EqualFold(getEnv("HTTPS_ENABLED", "false"), "true")
	logAddr := toLogAddress(addr)

	if httpsEnabled {
		certFile := os.Getenv("TLS_CERT_FILE")
		keyFile := os.Getenv("TLS_KEY_FILE")
		if certFile == "" || keyFile == "" {
			log.Fatal("TLS_CERT_FILE and TLS_KEY_FILE must be set when HTTPS_ENABLED=true")
		}

		log.Printf("voice room backend starting on https://%s", logAddr)
		if err := http.ListenAndServeTLS(addr, certFile, keyFile, nil); err != nil {
			log.Fatal("https server failed to start: ", err)
		}
		return
	}

	log.Printf("voice room backend starting on http://%s", logAddr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("http server failed to start: ", err)
	}
}

// getEnv 读取环境变量，不存在时返回默认值，便于启动配置统一走环境变量。
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// toLogAddress 把仅包含端口的监听地址转换成更易读的日志输出地址。
func toLogAddress(addr string) string {
	// 当监听地址写成 ":8080" 时，日志里补成 localhost:8080 更直观。
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
