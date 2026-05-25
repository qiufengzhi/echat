package handlers

import (
	"log"
	"net/http"

	"voice-room-backend/room"
)

// WebSocketHandler 把 HTTP 请求升级为 WebSocket，并把连接生命周期交给 room 包处理。
func WebSocketHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := room.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}

	room.HandleConnection(conn)
}
