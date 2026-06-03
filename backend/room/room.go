package room

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// StartCleanupLoop 启动一个后台协程，定时清理已经没有成员的房间。
func StartCleanupLoop() {
	go cleanupIdleRooms()
}

// createRoom 创建房间；如果房间已存在，则直接返回已有房间。
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

// getOrCreateRoom 先查找房间，不存在时再创建，避免调用方重复写判断逻辑。
func getOrCreateRoom(roomID string) *Room {
	roomLock.RLock()
	r, exists := allActiveRooms[roomID]
	roomLock.RUnlock()
	if exists {
		return r
	}

	return createRoom(roomID)
}

// HandleConnection 为新连接创建客户端对象，启动写协程，并持续读取该连接发来的消息。
func HandleConnection(conn *websocket.Conn) {
	userID := uuid.NewString()
	client := &Client{
		ID:   userID,
		Conn: conn,
		// 使用缓冲通道，避免短暂的慢客户端立刻阻塞整房广播。
		Send: make(chan []byte, 256),
	}

	clientLock.Lock()
	allConnectedClients[userID] = client
	clientLock.Unlock()

	log.Printf("client connected: %s", userID)

	go writePump(client) // 统一消息发送
	readPump(client)     // 统一消息读取
	disconnect(client)
}

// readPump 持续读取客户端发来的 WebSocket 消息，并分发给对应的业务处理函数。
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

		// 统一在读取协程里处理消息，保证同一连接上的消息顺序不被打乱。
		handleMessage(client, &msg)
	}
}

// writePump 持续消费客户端发送队列，把待发送消息写回对应的 WebSocket 连接。
func writePump(client *Client) {
	defer client.Conn.Close()

	// gorilla/websocket 连接不适合被多个协程同时写，因此统一走一个写协程。
	for message := range client.Send {
		if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}

	_ = client.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// handleMessage 根据消息类型把请求分发到加入房间、转发信令或离开房间等处理逻辑。
func handleMessage(client *Client, msg *Message) {
	switch msg.Type {
	case MsgTypeJoin: // 加入房间
		handleJoin(client, msg)
	case MsgTypeOffer: // 转发 WebRTC Offer
		handleRelay(client, MsgTypeOffer, msg.Payload)
	case MsgTypeAnswer: // 转发 WebRTC Answer
		handleRelay(client, MsgTypeAnswer, msg.Payload)
	case MsgTypeICE: // 转发 ICE 候选
		handleRelay(client, MsgTypeICE, msg.Payload)
	case MsgTypeLeave: // 离开房间
		handleLeave(client)
	case MsgTypePing: // 心跳响应 pong
		sendToClient(client, MsgTypePong, nil, client.RoomID)
	}
}

// handleJoin 把客户端加入指定房间，并在房间可用时通知双方开始交换 WebRTC 信令。
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

	// 根据房间id获取房间对象
	r := getOrCreateRoom(roomID)
	r.Lock.Lock()
	client.RoomID = roomID
	client.Username = username
	r.Clients[client.ID] = client
	userCount := len(r.Clients) // 当前房间内的用户数量
	r.Lock.Unlock()

	log.Printf("user %s joined room %s (count=%d)", username, roomID, userCount)

	if userCount == 1 {
		// 第一个进入房间的用户先等待，直到有第二个人加入。
		sendToClient(client, MsgTypeWaiting, nil, roomID)
		return
	}

	// 房间中已有其他人时，通知房间成员并允许当前用户开始信令协商。
	broadcastToRoom(roomID, client.ID, MsgTypeUserJoined, map[string]string{
		"user_id":  client.ID,
		"username": username,
	})

	// 通知客户端房间已有其他人。前端随后创建 RTCPeerConnection 并发送 Offer，两边开始 WebRTC 信令交换
	sendToClient(client, MsgTypeRoomReady, map[string]interface{}{
		"users":     getRoomUsers(roomID),
		"can_start": true,
	}, roomID)
}

// handleRelay 把 offer、answer、ice 这类 WebRTC 信令原样转发给同房间的其他成员。
func handleRelay(client *Client, msgType string, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	// 后端只负责信令转发，不解析 WebRTC 载荷内容。
	broadcastRawToRoom(client.RoomID, client.ID, msgType, payload)
	log.Printf("relayed %s from %s", msgType, client.ID[:8])
}

// handleLeave 处理客户端主动离开房间的请求，并复用统一的断连清理逻辑。
func handleLeave(client *Client) {
	log.Printf("user requested leave: %s", client.Username)
	disconnect(client)
}

// disconnect 清理客户端、房间成员关系和连接资源，并在需要时通知其他房间成员。
func disconnect(client *Client) {
	client.closeOnce.Do(func() {
		roomID := client.RoomID

		// 如果客户端已加入房间，则从房间中移除并通知其他成员
		if roomID != "" {
			var shouldDeleteRoom bool

			roomLock.RLock()
			r, ok := allActiveRooms[roomID]
			roomLock.RUnlock()
			if ok {
				r.Lock.Lock()
				delete(r.Clients, client.ID)
				remaining := len(r.Clients)
				r.Lock.Unlock()

				if remaining > 0 {
					// 房间里还有其他人时，通知他们当前用户已经离开。
					broadcastToRoom(roomID, client.ID, MsgTypeUserLeft, map[string]string{
						"user_id": client.ID,
					})
				} else {
					shouldDeleteRoom = true
				}
			}

			if shouldDeleteRoom {
				roomLock.Lock()
				if r, ok := allActiveRooms[roomID]; ok {
					r.Lock.Lock()
					empty := len(r.Clients) == 0
					r.Lock.Unlock()
					if empty {
						delete(allActiveRooms, roomID)
						log.Printf("room removed: %s", roomID)
					}
				}
				roomLock.Unlock()
			}
		}

		// 从客户端列表中移除客户端
		clientLock.Lock()
		delete(allConnectedClients, client.ID)
		clientLock.Unlock()

		// 关闭客户端的写协程
		close(client.Send)
		// 关闭客户端的连接
		_ = client.Conn.Close()
		log.Printf("client disconnected: %s", client.ID[:8])
	})
}

// sendToClient 把结构化消息编码后发送给指定客户端。
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

// sendError 向客户端发送统一格式的错误消息。
func sendError(client *Client, message string) {
	sendToClient(client, MsgTypeError, map[string]string{"message": message}, client.RoomID)
}

// sendRaw 把已经组装好的消息放入客户端发送队列，必要时丢弃慢客户端消息。
func sendRaw(client *Client, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("marshal message failed: %v", err)
		return
	}

	select {
	case client.Send <- data:
	default:
		// 慢客户端不再消费时直接丢弃，避免拖慢整个房间。
		log.Printf("dropping message for slow client: %s", client.ID[:8])
	}
}

// broadcastToRoom 把普通结构化消息广播给房间内除发送者外的所有成员。
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

// broadcastRawToRoom 把原始信令消息广播给房间内除发送者外的所有成员。
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

	// 先复制接收者列表，再逐个发送，避免网络写入时长期占用房间锁。
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
			// 单个慢客户端的阻塞不会影响房间里其他人的消息发送。
			log.Printf("dropping room message for slow client: %s", client.ID[:8])
		}
	}
}

// getRoomUsers 返回房间内当前所有用户的简要信息列表。
func getRoomUsers(roomID string) []map[string]string {
	roomLock.RLock()
	r, ok := allActiveRooms[roomID]
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

// parseUsername 从消息载荷中提取用户名，并做基本的空白裁剪。
func parseUsername(payload json.RawMessage) string {
	var username string
	if err := json.Unmarshal(payload, &username); err == nil {
		return strings.TrimSpace(username)
	}

	// 兼容直接传原始字符串的简单客户端。
	return strings.Trim(strings.TrimSpace(string(payload)), "\"")
}

// cleanupIdleRooms 定时扫描所有房间，把已经空掉的房间从内存中移除。
func cleanupIdleRooms() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		// 正常断开时也会删房间，这里主要兜底清理异常情况下残留的空房间。
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
