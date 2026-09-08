// membership.go 成员管理用例编排：举手上麦、审批、请下麦与静音
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
package signaling

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"echat-backend/room/aggregate"
	"echat-backend/room/events"
	"echat-backend/room/gateway"
)

// handleRaiseHand 听众举手请求上麦（成员天生可举手，不鉴权）
// 已在麦上（host/cohost/speaker）的成员重复举手被拒；举手写入内存并广播全房间
func handleRaiseHand(client *gateway.Client) {
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
	if role == aggregate.RoleHost || role == aggregate.RoleCohost || role == aggregate.RoleSpeaker {
		sendError(client, "已在麦上无需举手")
		return
	}
	r.Lock.Lock()
	r.WaitingOf[client.UserID] = true
	r.Lock.Unlock()

	recordMembershipEvent(r, roomID, events.EventTypeRoomHandRaised,
		map[string]any{"user_id": client.UserID, "username": client.Username})
	broadcastToRoom(roomID, client.ConnID, MsgTypeHandRaised, HandRaisedPayload{
		UserID:   client.UserID,
		Username: client.Username,
	})
}

// handleApproveMic 房主/副主持批准举手成员上麦（mod_mic 鉴权）
// 目标必须处于举手态；批准后 listener -> speaker 并投影 SpiceDB
func handleApproveMic(client *gateway.Client, payload json.RawMessage) {
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
	r.RoleOf[targetID] = aggregate.RoleSpeaker
	name, _ := memberName(r, targetID)
	r.Lock.Unlock()

	if prev != "" && prev != aggregate.RoleSpeaker {
		removeProjectedRole(r, targetID, prev)
	}
	assignRole(r, targetID, aggregate.RoleSpeaker)
	recordMembershipEvent(r, roomID, events.EventTypeRoomMicApproved,
		map[string]any{"user_id": targetID, "actor": client.UserID})
	broadcastToRoom(roomID, client.ConnID, MsgTypeMicApproved, RoleChangedPayload{
		UserID: targetID, Username: name, Role: aggregate.RoleSpeaker,
	})
}

// handleRejectMic 房主/副主持拒绝举手（mod_mic 鉴权），清理举手态并定向通知举手人
func handleRejectMic(client *gateway.Client, payload json.RawMessage) {
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
	_, targetConn := memberName(r, targetID)
	r.Lock.Unlock()

	recordMembershipEvent(r, roomID, events.EventTypeRoomMicRejected,
		map[string]any{"user_id": targetID, "actor": client.UserID})
	if targetConn != "" {
		if target := clientIn(r, targetConn); target != nil {
			sendToClient(target, MsgTypeMicRejected, MicRejectedPayload{UserID: targetID}, roomID)
		}
	}
}

// handleKickMic 房主/副主持请 speaker 下麦（mod_mic 鉴权），speaker -> listener 并投影
func handleKickMic(client *gateway.Client, payload json.RawMessage) {
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
	if r.RoleOf[targetID] != aggregate.RoleSpeaker {
		r.Lock.Unlock()
		sendError(client, "目标不在麦上")
		return
	}
	r.RoleOf[targetID] = aggregate.RoleListener
	delete(r.WaitingOf, targetID)
	name, _ := memberName(r, targetID)
	r.Lock.Unlock()

	removeProjectedRole(r, targetID, aggregate.RoleSpeaker)
	assignRole(r, targetID, aggregate.RoleListener)
	recordMembershipEvent(r, roomID, events.EventTypeRoomMicKicked,
		map[string]any{"user_id": targetID, "actor": client.UserID})
	broadcastToRoom(roomID, client.ConnID, MsgTypeMicKicked, RoleChangedPayload{
		UserID: targetID, Username: name, Role: aggregate.RoleListener,
	})
}

// handleMuteMic 房主/副主持静音/解除静音 speaker（mod_mic 鉴权），叠加 muted 关系覆盖 speak
func handleMuteMic(client *gateway.Client, payload json.RawMessage) {
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
	if r.RoleOf[targetID] != aggregate.RoleSpeaker {
		r.Lock.Unlock()
		sendError(client, "目标不在麦上")
		return
	}
	if muted {
		r.MutedOf[targetID] = true
	} else {
		delete(r.MutedOf, targetID)
	}
	name, _ := memberName(r, targetID)
	r.Lock.Unlock()

	if muted {
		assignRole(r, targetID, aggregate.RoleMuted)
	} else {
		removeProjectedRole(r, targetID, aggregate.RoleMuted)
	}
	recordMembershipEvent(r, roomID, events.EventTypeRoomMuted,
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
func manageContext(client *gateway.Client) (string, *aggregate.Room) {
	roomID := client.RoomID
	if roomID == "" {
		sendError(client, "join a room first")
		return "", nil
	}
	return roomID, getRoomByID(roomID)
}

// memberName 在房间锁已持有的前提下，返回目标成员昵称与其任一连接 ID
// 调用方必须已持有 r.Lock；连接 ID 用于定向通知（reject），空串表示目标已离场
func memberName(r *aggregate.Room, userID string) (name string, connID string) {
	for _, c := range r.Clients {
		if c.SessionUserID() == userID {
			return c.SessionUsername(), c.SessionConnID()
		}
	}
	return "", ""
}

// clientIn 按连接 ID 从房间成员表反查真实传输连接，用于定向消息
// 成员表以抽象 Session 存储，实际值即 gateway.Client，断言失败返回 nil
func clientIn(r *aggregate.Room, connID string) *gateway.Client {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	sess, ok := r.Clients[connID]
	if !ok {
		return nil
	}
	if c, ok := sess.(*gateway.Client); ok {
		return c
	}
	return nil
}

// recordMembershipEvent 记录成员管理领域事件到事务性 outbox（举手/审批/请下麦/静音）
// 失败仅告警，不阻断实时广播（实时优先策略）
func recordMembershipEvent(r *aggregate.Room, roomID string, eventType string, payload map[string]any) {
	if recorder == nil || r.AggID == "" {
		return
	}
	aggID, err := uuid.Parse(r.AggID)
	if err != nil {
		return
	}
	payload["room_code"] = roomID
	recorder.Record(context.Background(), events.RoomEvent{
		Type:        eventType,
		AggregateID: aggID,
		Subject:     events.Subject(aggID, strings.TrimPrefix(eventType, "room.")),
		Payload:     payload,
	})
}

// removeProjectedRole 移除某用户某个角色的 SpiceDB 元组（幂等，带超时保护主链路）
func removeProjectedRole(r *aggregate.Room, userID, role string) {
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	_ = roleProjector.Remove(ctx, r.AggID, userID, role)
}
