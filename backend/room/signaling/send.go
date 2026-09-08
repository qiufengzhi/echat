package signaling

import (
	"encoding/json"

	"echat-backend/room/gateway"
)

// sendToClient 把结构化 payload 编码成统一 Message 后发送给指定客户端
// payload 为 nil 时不携带 payload 字段
func sendToClient(client *gateway.Client, msgType string, payload interface{}, roomID string) {
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
		UserID:  client.UserID,
		Payload: rawPayload,
	})
}

// sendError 向客户端发送统一格式的错误消息
func sendError(client *gateway.Client, message string) {
	sendToClient(client, MsgTypeError, map[string]string{"message": message}, client.RoomID)
}

// sendRaw 把已经组装好的消息放入客户端发送队列，必要时丢弃慢客户端消息
func sendRaw(client *gateway.Client, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		logger.Warnw("消息序列化失败", "error", err)
		return
	}
	if !client.Enqueue(data) {
		logger.Warnw("慢客户端消息丢弃", "userID", client.ConnID[:8])
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
	r := getRoomByID(roomID)
	if r == nil {
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
	recipients := make([]*gateway.Client, 0, len(r.Clients))
	for id, sess := range r.Clients {
		if id != senderID {
			if c, ok := sess.(*gateway.Client); ok {
				recipients = append(recipients, c)
			}
		}
	}
	r.Lock.RUnlock()

	for _, client := range recipients {
		if !client.Enqueue(data) {
			logger.Warnw("房间广播消息丢弃(慢客户端)", "userID", client.ConnID[:8])
		}
	}
}
