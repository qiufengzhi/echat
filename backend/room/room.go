package room

import (
	"encoding/json"
	"log"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// StartCleanupLoop starts the idle room cleanup routine.
func StartCleanupLoop() {
	go cleanupIdleRooms()
}

func createRoom(roomID string) *Room {
	roomLock.Lock()
	defer roomLock.Unlock()

	if existing, ok := allActiveRooms[roomID]; ok {
		return existing
	}

	r := &Room{
		ID:      roomID,
		Clients: make(map[string]*Client),
	}
	allActiveRooms[roomID] = r
	log.Printf("room created: %s", roomID)
	return r
}

func getOrCreateRoom(roomID string) *Room {
	roomLock.RLock()
	r, exists := allActiveRooms[roomID]
	roomLock.RUnlock()
	if exists {
		return r
	}

	return createRoom(roomID)
}

// HandleConnection creates one client session per websocket connection.
func HandleConnection(conn *websocket.Conn) {
	userID := uuid.NewString()
	client := &Client{
		ID:   userID,
		Conn: conn,
		Send: make(chan []byte, 256),
	}

	clientLock.Lock()
	allConnectedClients[userID] = client
	clientLock.Unlock()

	log.Printf("client connected: %s", userID)

	go writePump(client)
	readPump(client)
	disconnect(client, "")
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
			closeCode := 0
			closeText := ""
			if closeErr, ok := err.(*websocket.CloseError); ok {
				closeCode = closeErr.Code
				closeText = closeErr.Text
			}
			log.Printf(
				"websocket read ended: client_id=%s room_id=%s username=%q close_code=%d close_text=%q err=%v time=%s",
				client.ID,
				client.RoomID,
				client.Username,
				closeCode,
				closeText,
				err,
				time.Now().Format(time.RFC3339),
			)
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
	case MsgTypeJoin:
		handleJoin(client, msg)
	case MsgTypeOffer:
		handleRelay(client, MsgTypeOffer, msg.Payload)
	case MsgTypeAnswer:
		handleRelay(client, MsgTypeAnswer, msg.Payload)
	case MsgTypeICE:
		handleRelay(client, MsgTypeICE, msg.Payload)
	case MsgTypeLeave:
		handleLeave(client, msg.Payload)
	case MsgTypePing:
		sendToClient(client, MsgTypePong, nil, client.RoomID)
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
	client.JoinedAt = time.Now()
	r.Clients[client.ID] = client
	if r.HostID == "" {
		r.HostID = client.ID
	}
	userCount := len(r.Clients)
	hostID := r.HostID
	r.Lock.Unlock()

	log.Printf("user %s joined room %s (count=%d host=%s)", username, roomID, userCount, hostID)

	if userCount == 1 {
		sendToClient(client, MsgTypeWaiting, WaitingPayload{HostID: hostID}, roomID)
		return
	}

	broadcastToRoom(roomID, client.ID, MsgTypeUserJoined, UserJoinedPayload{
		UserID:   client.ID,
		Username: username,
		HostID:   hostID,
	})

	sendToClient(client, MsgTypeRoomReady, RoomReadyPayload{
		Users:    getRoomUsers(roomID),
		HostID:   hostID,
		CanStart: true,
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

func handleLeave(client *Client, payload json.RawMessage) {
	var leavePayload LeavePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &leavePayload); err != nil {
			log.Printf("invalid leave payload from %s: %v", client.ID, err)
		}
	}

	log.Printf(
		"user requested leave: client_id=%s room_id=%s username=%q next_host=%q time=%s",
		client.ID,
		client.RoomID,
		client.Username,
		leavePayload.NextHostID,
		time.Now().Format(time.RFC3339),
	)
	disconnect(client, strings.TrimSpace(leavePayload.NextHostID))
}

func disconnect(client *Client, preferredNextHostID string) {
	client.closeOnce.Do(func() {
		roomID := client.RoomID

		if roomID != "" {
			var (
				shouldDeleteRoom bool
				nextHostID       string
			)

			roomLock.RLock()
			r, ok := allActiveRooms[roomID]
			roomLock.RUnlock()
			if ok {
				r.Lock.Lock()
				wasHost := r.HostID == client.ID
				delete(r.Clients, client.ID)
				remaining := len(r.Clients)

				if remaining == 0 {
					r.HostID = ""
					shouldDeleteRoom = true
				} else {
					if wasHost {
						r.HostID = chooseNextHostID(r, preferredNextHostID)
					}
					nextHostID = r.HostID
				}
				r.Lock.Unlock()

				if remaining > 0 {
					broadcastToRoom(roomID, client.ID, MsgTypeUserLeft, UserLeftPayload{
						UserID: client.ID,
						HostID: nextHostID,
					})

					if wasHost && nextHostID != "" && nextHostID != client.ID {
						broadcastToRoom(roomID, client.ID, MsgTypeHostChanged, map[string]string{
							"host_id": nextHostID,
						})
					}
				}
			}

			if shouldDeleteRoom {
				roomLock.Lock()
				if r, ok := allActiveRooms[roomID]; ok {
					r.Lock.RLock()
					empty := len(r.Clients) == 0
					r.Lock.RUnlock()
					if empty {
						delete(allActiveRooms, roomID)
						log.Printf("room removed: %s", roomID)
					}
				}
				roomLock.Unlock()
			}
		}

		clientLock.Lock()
		delete(allConnectedClients, client.ID)
		clientLock.Unlock()

		close(client.Send)
		_ = client.Conn.Close()
		log.Printf("client disconnected: %s", client.ID[:8])
	})
}

func chooseNextHostID(room *Room, preferredNextHostID string) string {
	if preferredNextHostID != "" {
		if _, ok := room.Clients[preferredNextHostID]; ok {
			return preferredNextHostID
		}
	}

	clientIDs := make([]string, 0, len(room.Clients))
	for id := range room.Clients {
		clientIDs = append(clientIDs, id)
	}

	if len(clientIDs) == 0 {
		return ""
	}

	slices.Sort(clientIDs)
	return clientIDs[rand.Intn(len(clientIDs))]
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
	sendToClient(client, MsgTypeError, map[string]string{"message": message}, client.RoomID)
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
	r, ok := allActiveRooms[roomID]
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

func getRoomUsers(roomID string) []RoomUser {
	roomLock.RLock()
	r, ok := allActiveRooms[roomID]
	roomLock.RUnlock()
	if !ok {
		return nil
	}

	r.Lock.RLock()
	clients := make([]*Client, 0, len(r.Clients))
	for _, client := range r.Clients {
		clients = append(clients, client)
	}
	r.Lock.RUnlock()

	slices.SortFunc(clients, func(a, b *Client) int {
		if a.JoinedAt.Before(b.JoinedAt) {
			return -1
		}
		if a.JoinedAt.After(b.JoinedAt) {
			return 1
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})

	users := make([]RoomUser, 0, len(clients))
	for _, client := range clients {
		users = append(users, RoomUser{
			ID:       client.ID,
			Username: client.Username,
		})
	}

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
		for id, r := range allActiveRooms {
			r.Lock.RLock()
			empty := len(r.Clients) == 0
			r.Lock.RUnlock()
			if empty {
				delete(allActiveRooms, id)
				log.Printf("idle room cleaned: %s", id)
			}
		}
		roomLock.Unlock()
	}
}
