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

// StartCleanupLoop 启动空房间清理协程。
// 正常离开时房间会立即尝试删除；这里主要兜底处理异常断开后残留的空房间。
func StartCleanupLoop() {
	go cleanupIdleRooms()
}

// createRoom 创建房间；如果房间已存在，则直接返回已有房间。
// roomID 是前端传入或生成的房间号，调用前应已做空值校验。
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
// 返回值始终是可用房间实例。
func getOrCreateRoom(roomID string) *Room {
	roomLock.RLock()
	r, exists := allActiveRooms[roomID]
	roomLock.RUnlock()
	if exists {
		return r
	}

	return createRoom(roomID)
}

// HandleConnection 为新 WebSocket 连接创建客户端对象，启动写协程，并持续读取客户端消息。
// conn 的生命周期由 readPump/writePump/disconnect 共同管理。
func HandleConnection(conn *websocket.Conn) {
	userID := uuid.NewString()
	client := &Client{
		ID:   userID,
		Conn: conn,
		// 使用缓冲队列避免短暂慢客户端立刻阻塞整房广播。
		Send: make(chan []byte, 256),
	}

	clientLock.Lock()
	allConnectedClients[userID] = client
	clientLock.Unlock()

	log.Printf("client connected: %s", userID)

	go writePump(client) // 统一串行写 WebSocket，避免并发写连接。
	readPump(client)     // 当前协程负责读取并按消息顺序分发。
	disconnect(client, "")
}

// readPump 持续读取客户端发来的 WebSocket 消息，并分发给对应业务处理函数。
// 同一连接上的消息在这里串行处理，保证 join/leave/offer 等顺序不被打乱。
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
			// 记录所有读循环结束原因，用来区分代理/网络断开和客户端主动 leave。
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

		// 统一在读取协程里处理消息，避免同一个客户端的状态被多个 goroutine 交叉修改。
		handleMessage(client, &msg)
	}
}

// writePump 持续消费客户端发送队列，把待发送消息写回对应的 WebSocket 连接。
// gorilla/websocket 不适合被多个协程同时写，因此一个客户端只保留一个写协程。
func writePump(client *Client) {
	defer client.Conn.Close()

	for message := range client.Send {
		if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}

	_ = client.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// handleMessage 根据消息类型把请求分发到加入房间、转发信令、离开房间或心跳响应。
func handleMessage(client *Client, msg *Message) {
	switch msg.Type {
	case MsgTypeJoin: // 加入房间
		handleJoin(client, msg)
	case MsgTypeOffer: // 转发 WebRTC Offer
		handleRelay(client, MsgTypeOffer, msg.Payload)
	case MsgTypeAnswer: // 转发 WebRTC Answer
		handleRelay(client, MsgTypeAnswer, msg.Payload)
	case MsgTypeICE: // 转发 ICE 候选地址
		handleRelay(client, MsgTypeICE, msg.Payload)
	case MsgTypeLeave: // 用户主动离开，可能携带房主交接目标
		handleLeave(client, msg.Payload)
	case MsgTypePing: // 心跳响应
		sendToClient(client, MsgTypePong, nil, client.RoomID)
	}
}

// handleJoin 把客户端加入指定房间，并在房间可用时通知成员开始 WebRTC 信令协商。
// 首位加入者会成为房主；后续加入者收到完整成员快照和当前房主 ID。
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

	// 加房间和设置房主必须在同一把房间锁内完成，避免并发加入时产生多个房主。
	r := getOrCreateRoom(roomID)
	r.Lock.Lock()
	client.RoomID = roomID
	client.Username = username
	client.JoinedAt = time.Now()
	r.Clients[client.ID] = client
	if r.HostID == "" {
		// 房间没有房主时，第一位成员自动成为房主。
		r.HostID = client.ID
	}
	userCount := len(r.Clients)
	hostID := r.HostID
	r.Lock.Unlock()

	log.Printf("user %s joined room %s (count=%d host=%s)", username, roomID, userCount, hostID)

	if userCount == 1 {
		// 第一个进入房间的用户先等待，同时拿到 host_id 用于前端展示“我是房主”。
		sendToClient(client, MsgTypeWaiting, WaitingPayload{HostID: hostID}, roomID)
		return
	}

	// 通知已有成员新用户已加入；新用户自己会在 room_ready 中拿到完整成员快照。
	broadcastToRoom(roomID, client.ID, MsgTypeUserJoined, UserJoinedPayload{
		UserID:   client.ID,
		Username: username,
		HostID:   hostID,
	})

	// 通知新加入者房间已有其他成员。前端随后可开始创建/响应 WebRTC 信令。
	sendToClient(client, MsgTypeRoomReady, RoomReadyPayload{
		Users:    getRoomUsers(roomID),
		HostID:   hostID,
		CanStart: true,
	}, roomID)
}

// handleRelay 把 offer、answer、ice 这类 WebRTC 信令原样转发给同房间其他成员。
// 后端只负责转发，不解析 SDP 或 ICE 内容。
func handleRelay(client *Client, msgType string, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	broadcastRawToRoom(client.RoomID, client.ID, msgType, payload)
	log.Printf("relayed %s from %s", msgType, client.ID[:8])
}

// handleLeave 处理客户端主动离开房间的请求。
// payload 可包含 next_host_id；服务端会在 disconnect 中校验该目标是否仍在线。
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

// disconnect 清理客户端、房间成员关系和连接资源，并在需要时通知其他房间成员。
// preferredNextHostID 只在离开者是当前房主时生效，且必须指向仍在房间内的成员。
func disconnect(client *Client, preferredNextHostID string) {
	client.closeOnce.Do(func() {
		roomID := client.RoomID

		// 如果客户端已加入房间，则先更新房间成员和房主，再广播离开事件。
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
					// 最后一位成员离开时清空房主，并在锁外删除空房间。
					r.HostID = ""
					shouldDeleteRoom = true
				} else {
					if wasHost {
						// 房主离开时优先使用指定交接对象，否则服务端选择一位剩余成员。
						r.HostID = chooseNextHostID(r, preferredNextHostID)
					}
					nextHostID = r.HostID
				}
				r.Lock.Unlock()

				if remaining > 0 {
					// user_left 总是广播，用于移除席位；HostID 让前端同步离开后的房主状态。
					broadcastToRoom(roomID, client.ID, MsgTypeUserLeft, UserLeftPayload{
						UserID: client.ID,
						HostID: nextHostID,
					})

					if wasHost && nextHostID != "" && nextHostID != client.ID {
						// host_changed 是更明确的房主变更事件，便于前端单独触发房主 UI 刷新。
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

		// 从全局客户端索引移除，避免后续诊断或广播误认为该连接仍在线。
		clientLock.Lock()
		delete(allConnectedClients, client.ID)
		clientLock.Unlock()

		// 关闭发送队列会让 writePump 退出；随后关闭底层 WebSocket。
		close(client.Send)
		_ = client.Conn.Close()
		log.Printf("client disconnected: %s", client.ID[:8])
	})
}

// chooseNextHostID 在当前房间剩余成员中选择下一任房主。
// preferredNextHostID 有效时优先使用；否则从排序后的成员 ID 中随机选择，排序让随机池稳定可观察。
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

// sendToClient 把结构化 payload 编码成统一 Message 后发送给指定客户端。
// payload 为 nil 时不携带 payload 字段。
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
// offer/answer/ice 会走这里，因为后端不需要解析 WebRTC 载荷。
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

	// 先复制接收者列表，再逐个发送，避免网络写入或慢客户端长期占用房间锁。
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
// 返回顺序按 JoinedAt 排列，让前端成员列表和席位展示尽量稳定。
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

	// 复制客户端后再排序，避免排序过程持有房间锁影响加入/离开。
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

// parseUsername 从 join 消息载荷中提取用户名，并做基本的空白裁剪。
// 兼容 JSON 字符串和少量直接传原始字符串的简单客户端。
func parseUsername(payload json.RawMessage) string {
	var username string
	if err := json.Unmarshal(payload, &username); err == nil {
		return strings.TrimSpace(username)
	}

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
