package handlers

import (
	"log"
	"net/http"

	"echat-backend/config"
	"echat-backend/room"

	"github.com/gorilla/websocket"
)

// WebSocketHandler 把 HTTP 请求升级为 WebSocket，并把连接生命周期交给 room 包处理
func WebSocketHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get().Room
	upgrader := websocket.Upgrader{
		ReadBufferSize:  cfg.WSReadBuffer,
		WriteBufferSize: cfg.WSWriteBuffer,
		CheckOrigin:     func(r *http.Request) bool { return cfg.WSCheckOrigin },
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}

	room.HandleConnection(conn)
}
