package handlers

import (
	"log"
	"net/http"

	"voice-room-backend/room"
)

func WebSocketHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := room.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}

	room.HandleConnection(conn)
}
