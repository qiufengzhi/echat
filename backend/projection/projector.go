// Package projection 事件消费与读模型投影：订阅 JetStream 上的 room.* 事件重建查询侧视图
//
// 定位：CQRS 读侧（事件骨干从「只写」变「可读」）——写侧是事务性 Outbox + relay（outbox 包），
// 读侧是 durable consumer + 幂等投影（本包），重复投递同一事件不产生重复数据
// 投影器各维护一个读模型：active_rooms / room:members:* / user_room_history
// 消费至少一次语义：Handle 返回 nil 才 Ack，否则 Nak 重投，处理须幂等
package projection

import (
	"context"
	"encoding/json"

	"echat-backend/logging"
)

// logger projection 消费包的具名日志器
var logger = logging.New("projection-consume")

// Event 一条从 JetStream 消费到的事件（对应 outbox relay 写入的信封解码后）
type Event struct {
	// ID 事件唯一 id（= outbox 主键），幂等与去重坐标
	ID string `json:"id"`
	// Type 事件类型，如 room.joined
	Type string `json:"type"`
	// AggregateID 聚合根 id 字符串（房间为 rooms.id）
	AggregateID string `json:"aggregate_id"`
	// OccurredAt 业务事务时间，RFC3339Nano
	OccurredAt string `json:"occurred_at"`
	// Payload 事件载荷，各事件键见 room/events 目录
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Projector 投影器：消费一条事件并维护一个读模型，事件至少一次、处理须幂等
type Projector interface {
	// Handle 处理一条事件更新读模型，重复事件幂等收敛，返回 nil 才 Ack
	Handle(ctx context.Context, ev Event) error
}
