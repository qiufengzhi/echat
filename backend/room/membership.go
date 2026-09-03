// membership.go 成员管理信令协议：举手上麦、审批、请下麦与静音
//
// 状态机：
//
//	listener --raise_hand--> waiting --approve_mic--> speaker
//	                             \--reject_mic--> (仍为 listener)
//	speaker --kick_mic--> listener
//	speaker --mute_mic(muted=true)--> speaker + muted（叠加，speak 被覆盖）
//	          --mute_mic(muted=false)--> speaker（解除）
//
// 举手态仅存内存（WaitingOf），角色变更投影 SpiceDB 并记 outbox 领域事件，失败均告警不阻断信令
package room

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// handleRaiseHand 听众举手请求上麦（成员天生可举手，不鉴权）
// 已在麦上（host/cohost/speaker）的成员重复举手被拒；举手写入内存并广播全房间
func handleRaiseHand(client *Client) {
	roomID := client.RoomID
	if roomID == "" {
		sendError(client, "join a room first")
		return
	}
	r := getRoomByID(roomID)
	if r == nil {
		return
	}
	r.Lock.RLock()
	role := r.RoleOf[client.UserID]
	r.Lock.RUnlock()
	if role == RoleHost || role == RoleCohost || role == RoleSpeaker {
		sendError(client, "已在麦上无需举手")
		return
	}
	r.Lock.Lock()
	r.WaitingOf[client.UserID] = true
	r.Lock.Unlock()

	persistMembershipEvent(roomID, r, EventTypeRoomHandRaised,
		map[string]any{"user_id": client.UserID, "username": client.Username})
	broadcastToRoom(roomID, client.ConnID, MsgTypeHandRaised, HandRaisedPayload{
		UserID:   client.UserID,
		Username: client.Username,
	})
}

// handleApproveMic 房主/副主持批准举手成员上麦（mod_mic 鉴权）
// 目标必须处于举手态；批准后 listener -> speaker 并投影 SpiceDB
func handleApproveMic(client *Client, payload json.RawMessage) {
	roomID, r := manageContext(client)
	targetID, _, ok := parseTargetUser(payload)
	if r == nil {
		return
	}
	if !ok {
		sendError(client, "invalid payload")
		return
	}
	if !canModMic(roomID, client.UserID) {
		sendError(client, "无权限管理麦克风")
		return
	}

	r.Lock.Lock()
	if !r.WaitingOf[targetID] {
		r.Lock.Unlock()
		sendError(client, "目标未举手")
		return
	}
	delete(r.WaitingOf, targetID)
	prev := r.RoleOf[targetID]
	r.RoleOf[targetID] = RoleSpeaker
	name, _ := roomMemberName(r, targetID)
	r.Lock.Unlock()

	if prev != "" && prev != RoleSpeaker {
		projectRemove(r, targetID, prev)
	}
	projectAssign(r, targetID, RoleSpeaker)
	persistMembershipEvent(roomID, r, EventTypeRoomMicApproved,
		map[string]any{"user_id": targetID, "actor": client.UserID})
	broadcastToRoom(roomID, client.ConnID, MsgTypeMicApproved, RoleChangedPayload{
		UserID: targetID, Username: name, Role: RoleSpeaker,
	})
}

// handleRejectMic 房主/副主持拒绝举手（mod_mic 鉴权），清理举手态并定向通知举手人
func handleRejectMic(client *Client, payload json.RawMessage) {
	roomID, r := manageContext(client)
	targetID, _, ok := parseTargetUser(payload)
	if r == nil {
		return
	}
	if !ok {
		sendError(client, "invalid payload")
		return
	}
	if !canModMic(roomID, client.UserID) {
		sendError(client, "无权限管理麦克风")
		return
	}
	r.Lock.Lock()
	if !r.WaitingOf[targetID] {
		r.Lock.Unlock()
		sendError(client, "目标未举手")
		return
	}
	delete(r.WaitingOf, targetID)
	_, targetConn := roomMemberName(r, targetID)
	r.Lock.Unlock()

	persistMembershipEvent(roomID, r, EventTypeRoomMicRejected,
		map[string]any{"user_id": targetID, "actor": client.UserID})
	if targetConn != "" {
		sendToClient(findClientByConnID(r, targetConn), MsgTypeMicRejected, MicRejectedPayload{UserID: targetID}, roomID)
	}
}

// handleKickMic 房主/副主持请 speaker 下麦（mod_mic 鉴权），speaker -> listener 并投影
func handleKickMic(client *Client, payload json.RawMessage) {
	roomID, r := manageContext(client)
	targetID, _, ok := parseTargetUser(payload)
	if r == nil {
		return
	}
	if !ok {
		sendError(client, "invalid payload")
		return
	}
	if !canModMic(roomID, client.UserID) {
		sendError(client, "无权限管理麦克风")
		return
	}
	r.Lock.Lock()
	if r.RoleOf[targetID] != RoleSpeaker {
		r.Lock.Unlock()
		sendError(client, "目标不在麦上")
		return
	}
	r.RoleOf[targetID] = RoleListener
	delete(r.WaitingOf, targetID)
	name, _ := roomMemberName(r, targetID)
	r.Lock.Unlock()

	projectRemove(r, targetID, RoleSpeaker)
	projectAssign(r, targetID, RoleListener)
	persistMembershipEvent(roomID, r, EventTypeRoomMicKicked,
		map[string]any{"user_id": targetID, "actor": client.UserID})
	broadcastToRoom(roomID, client.ConnID, MsgTypeMicKicked, RoleChangedPayload{
		UserID: targetID, Username: name, Role: RoleListener,
	})
}

// handleMuteMic 房主/副主持静音/解除静音 speaker（mod_mic 鉴权），叠加 muted 关系覆盖 speak
func handleMuteMic(client *Client, payload json.RawMessage) {
	roomID, r := manageContext(client)
	targetID, muted, ok := parseTargetUser(payload)
	if r == nil {
		return
	}
	if !ok {
		sendError(client, "invalid payload")
		return
	}
	if !canModMic(roomID, client.UserID) {
		sendError(client, "无权限管理麦克风")
		return
	}
	r.Lock.Lock()
	if r.RoleOf[targetID] != RoleSpeaker {
		r.Lock.Unlock()
		sendError(client, "目标不在麦上")
		return
	}
	if muted {
		r.MutedOf[targetID] = true
	} else {
		delete(r.MutedOf, targetID)
	}
	name, _ := roomMemberName(r, targetID)
	r.Lock.Unlock()

	if muted {
		projectAssign(r, targetID, RoleMuted)
	} else {
		projectRemove(r, targetID, RoleMuted)
	}
	persistMembershipEvent(roomID, r, EventTypeRoomMuted,
		map[string]any{"user_id": targetID, "muted": muted, "actor": client.UserID})
	broadcastToRoom(roomID, client.ConnID, MsgTypeMuted, MutedPayload{
		UserID: targetID, Username: name, Muted: muted,
	})
}

// parseTargetUser 解析管理消息的目标载荷，返回 目标用户ID、静音标记、是否有效
func parseTargetUser(payload json.RawMessage) (targetID string, muted bool, ok bool) {
	var t TargetUserPayload
	if len(payload) == 0 {
		return "", false, false
	}
	if err := json.Unmarshal(payload, &t); err != nil || t.TargetUserID == "" {
		return "", false, false
	}
	return t.TargetUserID, t.Muted, true
}

// manageContext 获取管理操作的房间上下文；未入房返回 nil 房间
func manageContext(client *Client) (string, *Room) {
	roomID := client.RoomID
	if roomID == "" {
		sendError(client, "join a room first")
		return "", nil
	}
	return roomID, getRoomByID(roomID)
}

// roomMemberName 在房间锁已持有的前提下，返回目标成员昵称与其任一连接 ID
// 调用方必须已持有 r.Lock；连接 ID 用于定向通知（reject），空串表示目标已离场
func roomMemberName(r *Room, userID string) (name string, connID string) {
	for _, c := range r.Clients {
		if c.UserID == userID {
			return c.Username, c.ConnID
		}
	}
	return "", ""
}

// findClientByConnID 按连接 ID 返回客户端对象，用于定向消息
func findClientByConnID(r *Room, connID string) *Client {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	return r.Clients[connID]
}

// persistMembershipEvent 记录成员管理领域事件到事务性 outbox（举手/审批/请下麦/静音）
// 失败仅告警，不阻断实时广播（实时优先策略）
func persistMembershipEvent(roomID string, r *Room, eventType string, payload map[string]any) {
	if pool == nil || r.AggID == "" {
		return
	}
	aggID, err := uuid.Parse(r.AggID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	payload["room_code"] = roomID
	if err := recordEvent(ctx, pool, RoomEvent{
		Type:        eventType,
		AggregateID: aggID,
		Subject:     roomSubject(aggID, strings.TrimPrefix(eventType, "room.")),
		Payload:     payload,
	}); err != nil {
		factErr("记账成员管理事件失败", roomID, err)
	}
}