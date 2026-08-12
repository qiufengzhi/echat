package room

import (
	"echat-backend/global"
	"encoding/json"
	"errors"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"echat-backend/config"
	"echat-backend/logging"
	"echat-backend/sfu"
)

var logger = logging.New("room")

// sfuServer 是全局 SFU 引擎实例，管理所有房间的 WebRTC PeerConnection 和音频转发
var sfuServer = sfu.NewSFUServer()

// StartCleanupLoop 启动空房间清理协程
// 正常离开时房间会立即尝试删除；这里主要兜底处理异常断开后残留的空房间
func StartCleanupLoop() {
	go cleanupIdleRooms()
}

// StartAIStateBroadcaster 启动协程，消费 global 的 AI 状态变更事件并广播给对应房间全体成员
// 唤醒词/休眠词/静默超时等发生在 sfu 与 global 包内的迁移，都靠它同步到前端
func StartAIStateBroadcaster() {
	go func() {
		for evt := range global.AIStateChangeCh {
			broadcastToRoom(evt.RoomID, "", MsgTypeAiStatus, AiToggleRes{
				State: evt.State.String(),
			})
		}
	}()
}

// createRoom 创建房间；如果房间已存在，则直接返回已有房间
// roomID 是前端传入或生成的房间号，调用前应已做空值校验
func createRoom(roomID string) *Room {
	roomLock.Lock()
	defer roomLock.Unlock()

	if existing, ok := allSignalRooms[roomID]; ok {
		return existing
	}

	r := &Room{
		ID:      roomID,
		Clients: make(map[string]*Client),
	}
	allSignalRooms[roomID] = r
	logger.Infow("房间已创建", "roomID", roomID)
	return r
}

// getOrCreateRoom 先查找房间，不存在时再创建，避免调用方重复写判断逻辑
// 返回值始终是可用房间实例
func getOrCreateRoom(roomID string) *Room {
	roomLock.RLock()
	r, exists := allSignalRooms[roomID]
	roomLock.RUnlock()
	if exists {
		return r
	}

	return createRoom(roomID)
}

// HandleConnection 为新 WebSocket 连接创建客户端对象，启动写协程，并持续读取客户端消息
// conn 的生命周期由 readPump/writePump/disconnect 共同管理
func HandleConnection(conn *websocket.Conn) {
	userID := uuid.NewString() // 客户端唯一标识
	client := &Client{
		ID:   userID,
		Conn: conn,
		// 使用缓冲队列避免短暂慢客户端立刻阻塞整房广播
		Send: make(chan []byte, 256),
	}

	clientLock.Lock()
	allConnectedClients[userID] = client
	clientLock.Unlock()

	logger.Infow("客户端已连接", "userID", userID)

	go writePump(client) // 统一串行写 WebSocket，避免并发写连接
	readPump(client)     // 当前协程负责读取并按消息顺序分发。阻塞

	// 正常情况下，readPump 会一直阻塞在 conn.ReadMessage()
	// 连接断开时，readPump 会退出循环并触发 disconnect
	disconnect(client, "")
}

// readPump 持续读取客户端发来的 WebSocket 消息，并分发给对应业务处理函数
// 同一连接上的消息在这里串行处理，保证 join/leave/sfu_answer 等顺序不被打乱
func readPump(client *Client) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warnw("readPump 发生 panic", "userID", client.ID, "panic", r)
		}
	}()

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			closeCode := 0
			closeText := ""
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				closeCode = closeErr.Code
				closeText = closeErr.Text
			}
			// 记录所有读循环结束原因，用来区分代理/网络断开和客户端主动 leave
			logger.Infow("WebSocket 读取结束",
				"userID", client.ID,
				"roomID", client.RoomID,
				"username", client.Username,
				"closeCode", closeCode,
				"closeText", closeText,
				"error", err,
			)
			return
		}

		var msg Message
		if err = json.Unmarshal(message, &msg); err != nil {
			logger.Warnw("无效消息", "userID", client.ID, "error", err)
			continue
		}

		// 统一在读取协程里处理消息，避免同一个客户端的状态被多个 goroutine 交叉修改
		handleMessage(client, &msg)
	}
}

// writePump 持续消费客户端发送队列，把待发送消息写回对应的 WebSocket 连接
// gorilla/websocket 不适合被多个协程同时写，因此一个客户端只保留一个写协程
func writePump(client *Client) {
	defer client.Conn.Close()

	for message := range client.Send {
		if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}

	_ = client.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// handleMessage 根据消息类型把请求分发到 SFU 信令处理、加入房间、离开房间或心跳响应
//
// SFU 信令流程（客户端发起 Offer）：
//
//	客户端 join → 服务端创建 Room + SFU PeerConnection → waiting/room_ready 回客户端
//	客户端创建 Offer → sfu_offer 给服务端 → 服务端创建 Answer → sfu_answer 给客户端
//	客户端收集到 ICE Candidate → sfu_ice 给服务端
//	SFU 引擎收集到 ICE Candidate → sfu_ice 给客户端
func handleMessage(client *Client, msg *Message) {
	switch msg.Type {
	case MsgTypeJoin: // 加入房间
		handleJoin(client, msg)
	case MsgTypeSFUOffer: // 客户端发起的 SDP Offer
		handleSFUOffer(client, msg.Payload)
	case MsgTypeSFUICE: // 客户端的 ICE Candidate
		handleSFUICE(client, msg.Payload)
	case MsgTypeRenegotiationAnswer: // 客户端对 renegotiation Offer 的 Answer
		handleRenegotiationAnswer(client, msg.Payload)
	case MsgTypeLeave: // 用户主动离开，可能携带房主交接目标
		handleLeave(client, msg.Payload)
	case MsgTypePing: // 心跳响应
		sendToClient(client, MsgTypePong, nil, client.RoomID)
	case MsgTypeAiToggle:
		handleAiToggle(client, msg)
	}
}

// handleAiToggle 处理 AI 助手开关请求，按开关更新房间 AI 状态
// 状态迁移事件由 StartAIStateBroadcaster 统一广播给全房间，此处不再单独回复
func handleAiToggle(client *Client, msg *Message) {
	var req AiToggleReq
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		sendError(client, "invalid payload")
		return
	}

	roomID := client.RoomID
	if req.Enable {
		global.AIStates.SetOnline(roomID) // 开启：直接进入在线状态
	} else {
		global.AIStates.SetOffline(roomID) // 关闭：回到离线状态
	}
}

// handleJoin 把客户端加入指定房间，创建 SFU PeerConnection（不生成 Offer）
//
// SFU 信令流程（客户端发起 Offer）：
//  1. 客户端 join → 服务端创建房间并创建 SFU PeerConnection
//  2. 服务端返回 waiting 或 room_ready → 客户端创建 Offer
//  3. 客户端发送 sfu_offer → 服务端通过 AcceptOffer 回复 sfu_answer
//  4. SFU <-> 客户端交换 ICE Candidate（sfu_ice）
//  5. 音轨到达 SFU → 自动转发给其他成员
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

	// 加入信令房间（设置成员和房主）
	r := getOrCreateRoom(roomID)
	r.Lock.Lock()
	client.RoomID = roomID
	client.Username = username
	client.JoinedAt = time.Now()
	r.Clients[client.ID] = client
	if r.HostID == "" {
		r.HostID = client.ID
	}
	hostID := r.HostID
	userCount := len(r.Clients)
	r.Lock.Unlock()

	logger.Infow("用户已加入房间",
		"username", username, "roomID", roomID, "userCount", userCount, "hostID", hostID,
	)

	// --- SFU 集成：创建 PeerConnection，但不生成 Offer ---
	// Offer 由客户端发起，服务端收到 sfu_offer 后通过 AcceptOffer 创建 Answer

	sfuRoom := sfuServer.GetOrCreateRoom(roomID)

	// 注册 ICE Candidate 回调：SFU 引擎收集到 candidate 后通过 WebSocket 转发给客户端
	sfuRoom.SetOnICECandidate(func(clientID string, candidate webrtc.ICECandidateInit) {
		roomLock.RLock()
		signalRoom, ok := allSignalRooms[roomID]
		roomLock.RUnlock()
		if !ok {
			return
		}
		signalRoom.Lock.RLock()
		targetClient, ok := signalRoom.Clients[clientID]
		signalRoom.Lock.RUnlock()
		if !ok {
			return
		}
		payload := SFUPayloadFromICECandidateInit(candidate)
		sendToClient(targetClient, MsgTypeSFUICE, payload, roomID)
	})

	// 注册 renegotiation 回调：当 SFU 向订阅者添加中继音轨后，需要向该客户端发送 renegotiation Offer
	sfuRoom.SetOnRenegotiation(func(clientID string, offerSDP string) {
		roomLock.RLock()
		signalRoom, ok := allSignalRooms[roomID]
		roomLock.RUnlock()
		if !ok {
			return
		}
		signalRoom.Lock.RLock()
		targetClient, ok := signalRoom.Clients[clientID]
		signalRoom.Lock.RUnlock()
		if !ok {
			return
		}
		sendToClient(targetClient, MsgTypeRenegotiationOffer, RenegotiationOfferPayload{SDP: offerSDP}, roomID)
	})

	// 让 SFU 引擎为该客户端创建 PeerConnection（不生成 Offer）
	if err := sfuRoom.Join(client.ID); err != nil {
		logger.Warnw("加入失败", "userID", client.ID[:8], "error", err)
		sendError(client, "无法创建 WebRTC 连接，请重试")
		return
	}

	// --- 房间状态广播 ---

	if userCount == 1 {
		// 首位成员：发送 waiting，告知其是房主
		sendToClient(client, MsgTypeWaiting, WaitingPayload{HostID: hostID}, roomID)
		return
	}

	// 通知已有成员新用户已加入
	broadcastToRoom(roomID, client.ID, MsgTypeUserJoined, UserJoinedPayload{
		UserID:   client.ID,
		Username: username,
		HostID:   hostID,
	})

	// 通知新加入者房间成员快照
	sendToClient(client, MsgTypeRoomReady, RoomReadyPayload{
		Users:    getRoomUsers(roomID),
		HostID:   hostID,
		CanStart: true,
	}, roomID)
}

// handleSFUOffer 处理客户端发来的 SDP Offer，通过 SFU 引擎创建 Answer 并返回
func handleSFUOffer(client *Client, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	var offer SFUOfferPayload
	if err := json.Unmarshal(payload, &offer); err != nil {
		logger.Warnw("sfu_offer 内容无效", "userID", client.ID[:8], "error", err)
		return
	}

	sfuRoom := sfuServer.GetRoom(client.RoomID)
	if sfuRoom == nil {
		logger.Warnw("SFU 房间未找到", "roomID", client.RoomID)
		return
	}

	answerSDP, err := sfuRoom.AcceptOffer(client.ID, offer.SDP)
	if err != nil {
		logger.Warnw("接受 Offer 失败", "userID", client.ID[:8], "error", err)
		sendError(client, "信令协商失败")
		return
	}

	// 将 Answer SDP 通过 sfu_answer 发回客户端
	sendToClient(client, MsgTypeSFUAnswer, SFUAnswerPayload{SDP: answerSDP}, client.RoomID)
}

// handleRenegotiationAnswer 处理客户端对 renegotiation Offer 的 Answer，交回 SFU 引擎处理
func handleRenegotiationAnswer(client *Client, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	var answer RenegotiationAnswerPayload
	if err := json.Unmarshal(payload, &answer); err != nil {
		logger.Warnw("renegotiation answer 内容无效", "userID", client.ID[:8], "error", err)
		return
	}

	sfuRoom := sfuServer.GetRoom(client.RoomID)
	if sfuRoom == nil {
		logger.Warnw("SFU 房间未找到", "roomID", client.RoomID)
		return
	}

	if err := sfuRoom.AcceptRenegotiationAnswer(client.ID, answer.SDP); err != nil {
		logger.Warnw("renegotiation answer 处理失败", "userID", client.ID[:8], "error", err)
		return
	}
}

// handleSFUICE 处理客户端发来的 ICE Candidate，传递给 SFU 引擎
func handleSFUICE(client *Client, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	var ice SFUICEPayload
	if err := json.Unmarshal(payload, &ice); err != nil {
		logger.Warnw("sfu_ice 内容无效", "userID", client.ID[:8], "error", err)
		return
	}

	sfuRoom := sfuServer.GetRoom(client.RoomID)
	if sfuRoom == nil {
		logger.Warnw("SFU 房间未找到", "roomID", client.RoomID)
		return
	}

	if err := sfuRoom.AcceptICECandidate(client.ID, ice.ToWebRTCICECandidateInit()); err != nil {
		logger.Warnw("ICE Candidate 添加失败", "userID", client.ID[:8], "error", err)
	}
}

// handleRelay 把 offer、answer、ice 这类 WebRTC 信令原样转发给同房间其他成员
// 已弃用，由 SFU 替代。保留以供旧客户端兼容，当前不被 handleMessage 调用
func handleRelay(client *Client, msgType string, payload json.RawMessage) {
	if client.RoomID == "" {
		sendError(client, "join a room before signaling")
		return
	}

	broadcastRawToRoom(client.RoomID, client.ID, msgType, payload)
	logger.Infow("已转发信令", "msgType", msgType, "from", client.ID[:8])
}

// handleLeave 处理客户端主动离开房间的请求
// payload 可包含 next_host_id；服务端会在 disconnect 中校验该目标是否仍在线
func handleLeave(client *Client, payload json.RawMessage) {
	var leavePayload LeavePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &leavePayload); err != nil {
			logger.Warnw("离开消息无效", "userID", client.ID, "error", err)
		}
	}

	logger.Infow("用户请求离开",
		"userID", client.ID,
		"roomID", client.RoomID,
		"username", client.Username,
		"nextHost", leavePayload.NextHostID,
	)
	disconnect(client, strings.TrimSpace(leavePayload.NextHostID))
}

// disconnect 清理客户端、房间成员关系、SFU PeerConnection 和连接资源
// preferredNextHostID 只在离开者是当前房主时生效，且必须指向仍在房间内的成员
func disconnect(client *Client, preferredNextHostID string) {
	client.closeOnce.Do(func() {
		roomID := client.RoomID

		// 先清理 SFU 连接，确保停止音轨转发
		if roomID != "" {
			if sfuRoom := sfuServer.GetRoom(roomID); sfuRoom != nil {
				sfuRoom.Leave(client.ID)
				// 如果 SFU 房间已空，也清理 SFU 房间
				if sfuRoom.PeerCount() == 0 {
					sfuServer.RemoveRoom(roomID)
				}
			}
		}

		// 如果客户端已加入房间，则先更新房间成员和房主，再广播离开事件
		if roomID != "" {
			var (
				shouldDeleteRoom bool   // 房间无人时删除整个房间
				nextHostID       string // 新房主 ID，用于广播给剩余成员
			)

			roomLock.RLock()
			r, ok := allSignalRooms[roomID]
			roomLock.RUnlock()
			if ok {
				r.Lock.Lock()
				wasHost := r.HostID == client.ID // 是否为房主
				delete(r.Clients, client.ID)
				remaining := len(r.Clients)

				if remaining == 0 { // 房间无人，删除整个房间
					r.HostID = ""
					shouldDeleteRoom = true
				} else {
					if wasHost { // 房主离开，选择新房主
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
						global.AIStates.SetOffline(roomID) // 房主交接时重置 AI 为离线，新房主需重新开启
						broadcastToRoom(roomID, client.ID, MsgTypeHostChanged, map[string]string{
							"host_id": nextHostID,
						})
					}
				}
			}

			if shouldDeleteRoom {
				roomLock.Lock()
				if r, ok := allSignalRooms[roomID]; ok {
					r.Lock.RLock()
					empty := len(r.Clients) == 0
					r.Lock.RUnlock()
					if empty {
						delete(allSignalRooms, roomID)
						global.AIStates.Remove(roomID) // 清理房间 AI 状态，避免状态泄漏
						logger.Infow("房间已删除", "roomID", roomID)
					}
				}
				roomLock.Unlock()
			}
		}

		// 从全局客户端索引移除
		clientLock.Lock()
		delete(allConnectedClients, client.ID)
		clientLock.Unlock()

		// 关闭发送队列和底层 WebSocket
		close(client.Send)
		_ = client.Conn.Close()
		logger.Infow("客户端已断开", "userID", client.ID[:8])
	})
}

// chooseNextHostID 在当前房间剩余成员中选择下一任房主
// preferredNextHostID 有效时优先使用；否则从排序后的成员 ID 中随机选择
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

// sendToClient 把结构化 payload 编码成统一 Message 后发送给指定客户端
// payload 为 nil 时不携带 payload 字段
func sendToClient(client *Client, msgType string, payload interface{}, roomID string) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			logger.Warnw("payload 序列化失败", "msgType", msgType, "error", err)
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

// sendError 向客户端发送统一格式的错误消息
func sendError(client *Client, message string) {
	sendToClient(client, MsgTypeError, map[string]string{"message": message}, client.RoomID)
}

// sendRaw 把已经组装好的消息放入客户端发送队列，必要时丢弃慢客户端消息
func sendRaw(client *Client, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		logger.Warnw("消息序列化失败", "error", err)
		return
	}

	select {
	case client.Send <- data:
	default:
		logger.Warnw("慢客户端消息丢弃", "userID", client.ID[:8])
	}
}

// broadcastToRoom 把普通结构化消息广播给房间内除发送者外的所有成员
func broadcastToRoom(roomID, senderID, msgType string, payload interface{}) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			logger.Warnw("广播 payload 序列化失败", "msgType", msgType, "error", err)
			return
		}
		rawPayload = data
	}
	broadcastRawToRoom(roomID, senderID, msgType, rawPayload)
}

// broadcastRawToRoom 把原始信令消息广播给房间内除发送者外的所有成员
func broadcastRawToRoom(roomID, senderID, msgType string, payload json.RawMessage) {
	roomLock.RLock()
	r, ok := allSignalRooms[roomID]
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
		logger.Warnw("广播消息序列化失败", "error", err)
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
			logger.Warnw("房间广播消息丢弃(慢客户端)", "userID", client.ID[:8])
		}
	}
}

// getRoomUsers 返回房间内当前所有用户的简要信息列表
// 返回顺序按 JoinedAt 排列，让前端成员列表和席位展示尽量稳定
func getRoomUsers(roomID string) []RoomUser {
	roomLock.RLock()
	r, ok := allSignalRooms[roomID]
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

// parseUsername 从 join 消息载荷中提取用户名，并做基本的空白裁剪
// 兼容 JSON 字符串和少量直接传原始字符串的简单客户端
func parseUsername(payload json.RawMessage) string {
	var username string
	if err := json.Unmarshal(payload, &username); err == nil {
		return strings.TrimSpace(username)
	}

	return strings.Trim(strings.TrimSpace(string(payload)), "\"")
}

// cleanupIdleRooms 定时扫描所有房间，清理空房间。扫描间隔来自配置文件的 room.idle_timeout。
func cleanupIdleRooms() {
	interval, _ := time.ParseDuration(config.Get().Room.IdleTimeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		roomLock.Lock()
		for id, r := range allSignalRooms {
			r.Lock.RLock()
			empty := len(r.Clients) == 0
			r.Lock.RUnlock()
			if empty {
				delete(allSignalRooms, id)
				logger.Infow("清理空闲房间", "roomID", id)
			}
		}
		roomLock.Unlock()
	}
}
