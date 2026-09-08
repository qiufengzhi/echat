// Package events 房间域事件目录：事件类型、事件载体与 subject 编排
//
// 纯领域层：只定义事件「是什么」，不关心事件如何落库/投递
// 写库侧由 room/persist 在事实事务内完成，消费侧由 projection 订阅 JetStream 重建读模型
package events

import "github.com/google/uuid"

// 房间域事件类型：subject 规则 room.{聚合根id}.{后缀}，供 outbox 记账与下游投影消费对齐
const (
	// EventTypeRoomCreated 房间随首位成员加入而创建
	EventTypeRoomCreated = "room.created"
	// EventTypeRoomJoined 成员加入房间
	EventTypeRoomJoined = "room.joined"
	// EventTypeRoomLeft 成员离开房间（主动离开或断线）
	EventTypeRoomLeft = "room.left"
	// EventTypeRoomHostTransferred 房主交接：原房主离开，新一任房主上任
	EventTypeRoomHostTransferred = "room.host_transferred"
	// EventTypeRoomClosed 房间清空关闭，归档
	EventTypeRoomClosed = "room.closed"
	// EventTypeRoomAiToggled AI 语音助手开关变更
	EventTypeRoomAiToggled = "room.ai_toggled"
	// EventTypeRoomHandRaised 成员举手请求上麦
	EventTypeRoomHandRaised = "room.hand_raised"
	// EventTypeRoomMicApproved 房主批准成员上麦
	EventTypeRoomMicApproved = "room.mic_approved"
	// EventTypeRoomMicRejected 房主拒绝成员上麦
	EventTypeRoomMicRejected = "room.mic_rejected"
	// EventTypeRoomMicKicked 房主请成员下麦
	EventTypeRoomMicKicked = "room.mic_kicked"
	// EventTypeRoomMuted 房主静音/解除静音成员
	EventTypeRoomMuted = "room.muted"
)

// RoomEvent 一条房间域领域事件，由事实写库侧组装后随事务记入 outbox
type RoomEvent struct {
	// Type 事件类型取值见上方 EventType* 枚举
	Type string
	// AggregateID 聚合根 id（rooms.id），不对外展示，事件骨干与授权投影的编排坐标
	AggregateID uuid.UUID
	// Subject 事件骨干 subject，按 room.{id}.{后缀} 编排
	Subject string
	// Payload 事件载荷 jsonb，键与取值按各事件约定
	Payload map[string]any
}

// Subject 拼接 room.{id}.{suffix} 形式的 JetStream subject
// aggID 房间聚合根 id，suffix 事件后缀（如 joined），返回完整 subject 串
func Subject(aggID uuid.UUID, suffix string) string {
	return "room." + aggID.String() + "." + suffix
}
