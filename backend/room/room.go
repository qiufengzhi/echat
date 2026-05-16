package room

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Client struct {
	ID        string
	RoomID    string
	Username  string
	Conn      *websocket.Conn
	Send      chan []byte
	closeOnce sync.Once
}

type Room struct {
	ID      string
	Clients map[string]*Client
	Lock    sync.RWMutex
}

type Message struct {
	Type    string          `json:"type"`
	RoomID  string          `json:"room_id"`
	UserID  string          `json:"user_id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

var (
	rooms      = make(map[string]*Room)
	clients    = make(map[string]*Client)
	roomLock   sync.RWMutex
	clientLock sync.RWMutex

	Upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)

func StartCleanupLoop() {
	go cleanupIdleRooms()
}

func createRoom(roomID string) *Room {
	roomLock.Lock()
	defer roomLock.Unlock()

	if existing, ok := rooms[roomID]; ok {
		return existing
	}

	r := &Room{
		ID:      roomID,
		Clients: make(map[string]*Client),
	}
	rooms[roomID] = r
	log.Printf("room created: %s", roomID)
	return r
}

func getOrCreateRoom(roomID string) *Room {
	roomLock.RLock()
	r, exists := rooms[roomID]
	roomLock.RUnlock()
	if exists {
		return r
	}

	return createRoom(roomID)
}

func HandleConnection(conn *websocket.Conn) {
	userID := uuid.NewString()
	client := &Client{
		ID:   userID,
		Conn: conn,
		Send: make(chan []byte, 256),
	}

	clientLock.Lock()
	clients[userID] = client
	clientLock.Unlock()

	log.Printf("client connected: %s", userID)

	go writePump(client)
	readPump(client)
	disconnect(client)
}

func readPump(client *Client) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("readPump panic for %s: %v", client.ID, r)
		}
	}()

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("read message failed for %s: %v", client.ID, err)
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("invalid message from %s: %v", client.ID, err)
			continue
		}

		handleMessage(client, &msg)
	}
}

func writePump(client *Client) {
	defer client.Conn.Close()

	for message := range client.Send {
		if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}

	_ = client.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func handleMessage(client *Client, msg *Message) {
	switch msg.Type {
	case "join":
		handleJoin(client, msg)
	case "offer":
		handleRelay(client, "offer", msg.Payload)
	case "answer":
		handleRelay(client, "answer", msg.Payload)
	case "ice":
		handleRelay(client, "ice", msg.Payload)
	case "leave":
		handleLeave(client)
	case "ping":
		sendToClient(client, "pong", nil, client.RoomID)
	}
}

func handleJoin(client *Client, msg *Message) {
	roomID := strings.TrimSpace(msg.RoomID)
	if roomID == "" {
		sendError(client, "room_id is required")
		return
	}

	username := parseUsername(msg.Payload)
	if username == "" {
		username = "用户" + client.ID[:8]
	}

	r := getOrCreateRoom(roomID)
	r.Lock.Lock()
	client.RoomID = roomID
	client.Username = username
	r.Clients[client.ID] = client
	userCount := len(r.Clients)
	r.Lock.Unlock()

	log.Printf("user %s joined room %s (count=%d)", username, roomID, userCount)

	if userCount == 1 {
		sendToClient(client, "waiting", nil, roomID)
		return
	}

	broadcastToRoom(roomID, client.ID, "user_joined", map[string]string{
		"user_id":  client.ID,
		"username": username,
	})
	sendToClient(client, "room_ready", map[string]interface{}{
		"users":     getRoomUsers(roomID),
		"can_start": true,
	}, roomID)
}

func handleRelay(client *Client, msgType string, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	broadcastRawToRoom(client.RoomID, client.ID, msgType, payload)
	log.Printf("relayed %s from %s", msgType, client.ID[:8])
}

func handleLeave(client *Client) {
	log.Printf("user requested leave: %s", client.Username)
	disconnect(client)
}

func disconnect(client *Client) {
	client.closeOnce.Do(func() {
		roomID := client.RoomID
		if roomID != "" {
			var shouldDeleteRoom bool

			roomLock.RLock()
			r, ok := rooms[roomID]
			roomLock.RUnlock()
			if ok {
				r.Lock.Lock()
				delete(r.Clients, client.ID)
				remaining := len(r.Clients)
				r.Lock.Unlock()

				if remaining > 0 {
					broadcastToRoom(roomID, client.ID, "user_left", map[string]string{
						"user_id": client.ID,
					})
				} else {
					shouldDeleteRoom = true
				}
			}

			if shouldDeleteRoom {
				roomLock.Lock()
				if r, ok := rooms[roomID]; ok {
					r.Lock.Lock()
					empty := len(r.Clients) == 0
					r.Lock.Unlock()
					if empty {
						delete(rooms, roomID)
						log.Printf("room removed: %s", roomID)
					}
				}
				roomLock.Unlock()
			}
		}

		clientLock.Lock()
		delete(clients, client.ID)
		clientLock.Unlock()

		close(client.Send)
		_ = client.Conn.Close()
		log.Printf("client disconnected: %s", client.ID[:8])
	})
}

func sendToClient(client *Client, msgType string, payload interface{}, roomID string) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			log.Printf("marshal payload failed for %s: %v", msgType, err)
			return
		}
		rawPayload = data
	}

	sendRaw(client, Message{
		Type:    msgType,
		RoomID:  roomID,
		UserID:  client.ID,
		Payload: rawPayload,
	})
}

func sendError(client *Client, message string) {
	sendToClient(client, "error", map[string]string{"message": message}, client.RoomID)
}

func sendRaw(client *Client, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("marshal message failed: %v", err)
		return
	}

	select {
	case client.Send <- data:
	default:
		log.Printf("dropping message for slow client: %s", client.ID[:8])
	}
}

func broadcastToRoom(roomID, senderID, msgType string, payload interface{}) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			log.Printf("marshal broadcast payload failed for %s: %v", msgType, err)
			return
		}
		rawPayload = data
	}

	broadcastRawToRoom(roomID, senderID, msgType, rawPayload)
}

func broadcastRawToRoom(roomID, senderID, msgType string, payload json.RawMessage) {
	roomLock.RLock()
	r, ok := rooms[roomID]
	roomLock.RUnlock()
	if !ok {
		return
	}

	msg := Message{
		Type:    msgType,
		RoomID:  roomID,
		UserID:  senderID,
		Payload: payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("marshal broadcast message failed: %v", err)
		return
	}

	r.Lock.RLock()
	recipients := make([]*Client, 0, len(r.Clients))
	for id, client := range r.Clients {
		if id != senderID {
			recipients = append(recipients, client)
		}
	}
	r.Lock.RUnlock()

	for _, client := range recipients {
		select {
		case client.Send <- data:
		default:
			log.Printf("dropping room message for slow client: %s", client.ID[:8])
		}
	}
}

func getRoomUsers(roomID string) []map[string]string {
	roomLock.RLock()
	r, ok := rooms[roomID]
	roomLock.RUnlock()
	if !ok {
		return nil
	}

	r.Lock.RLock()
	users := make([]map[string]string, 0, len(r.Clients))
	for id, client := range r.Clients {
		users = append(users, map[string]string{
			"id":       id,
			"username": client.Username,
		})
	}
	r.Lock.RUnlock()

	return users
}

func parseUsername(payload json.RawMessage) string {
	var username string
	if err := json.Unmarshal(payload, &username); err == nil {
		return strings.TrimSpace(username)
	}

	return strings.Trim(strings.TrimSpace(string(payload)), "\"")
}

func cleanupIdleRooms() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		roomLock.Lock()
		for id, r := range rooms {
			r.Lock.RLock()
			empty := len(r.Clients) == 0
			r.Lock.RUnlock()
			if empty {
				delete(rooms, id)
				log.Printf("idle room cleaned: %s", id)
			}
		}
		roomLock.Unlock()
	}
}
