package aggregate

import (
	"context"

	"echat-backend/room/events"
)

// FactStore 事实落库器：把聚合收敛后的当前态与伴随领域事件同事务写库
// 实现见 room/persist；写失败按实时优先策略告警即可，不阻塞信令主链路
type FactStore interface {
	// JoinRoom 落一条成员加入事实：按需建/复开房间行、upsert 成员在场，并同事务记账 room.created/room.joined
	JoinRoom(ctx context.Context, room *Room, member Session) error
	// LeaveRoom 落一条成员离开事实：标记离场、交接房主或关闭清空房间，并同事务记账 room.left 等
	// nextHostID 为交接目标，空表示房主未变（多端摘连接）或房间清空，此时不记房间交接事件
	LeaveRoom(ctx context.Context, room *Room, leaverUserID string, wasHost bool, nextHostID string, shouldDelete bool, reason string) error
}

// EventRecorder 事件记账器：把孤立领域事件写入 outbox（不与事实同事务的场景）
// 实现见 room/persist；供举手/上麦审批/AI 开关等即时记账使用
type EventRecorder interface {
	// Record 记账一条事件到 outbox，调用方已组装好聚合根 id 与 payload
	Record(ctx context.Context, ev events.RoomEvent) error
}

// RoleProjector 授权投影器：把角色变更写入 SpiceDB（write-through，幂等）
// 实现见 room/projection；AuthZ 未启用时实现内部判空降级返回 nil
type RoleProjector interface {
	// Assign 投影某用户获得某角色（TOUCH 幂等）
	Assign(ctx context.Context, aggID string, userID string, role string) error
	// Remove 投影某用户移除某角色（DELETE 幂等，不存在视为成功）
	Remove(ctx context.Context, aggID string, userID string, role string) error
	// TransferHost 原子交接房主：删旧 host 写新 host，保证任意时刻唯一房主
	TransferHost(ctx context.Context, aggID string, from string, to string) error
}
