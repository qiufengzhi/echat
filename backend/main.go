package main

import (
	"log"
	"net/http"

	"voice-room-backend/handlers"
	"voice-room-backend/room"
)

func main() {
	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/ws", handlers.WebSocketHandler)

	room.StartCleanupLoop()

	addr := ":8080"
	log.Printf("voice room backend starting on http://localhost%s", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("server failed to start: ", err)
	}
}
