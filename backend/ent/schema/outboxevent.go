// schema/outboxevent.go 定义事务性 Outbox 表，对应蓝图 §3.2
//
// 业务数据与领域事件在同一事务落库，保证「双写一致性」：
// 提交成功即代表事件已记账，由独立 relay 进程把 pending 事件投递到 NATS JetStream
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// OutboxEventStatusPending 待发送：已随业务事务提交，等待 relay 投递
const OutboxEventStatusPending = "pending"

// OutboxEventStatusSent 已发送：relay 已成功投递到事件骨干
const OutboxEventStatusSent = "sent"

// OutboxEventStatusFailed 发送失败：投递多次失败，进入治理流程
const OutboxEventStatusFailed = "failed"

// OutboxEvent 一条待投递的领域事件，与业务数据同事务写入
type OutboxEvent struct {
	ent.Schema
}

// Fields 返回 OutboxEvent 的字段定义
func (OutboxEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("事件主键，写入时由服务端生成"),
		field.String("event_type").
			Comment("事件类型枚举，如 user.registered / user.login.succeeded"),
		field.UUID("aggregate_id", uuid.UUID{}).
			Comment("聚合根 id，如 user_id，供下游按实例归集"),
		field.String("subject").
			Comment("事件骨干 subject，如 user.{id}.registered"),
		field.JSON("payload", map[string]any{}).
			Optional().
			Comment("事件载荷 jsonb，可空"),
		field.Enum("status").
			Values(OutboxEventStatusPending, OutboxEventStatusSent, OutboxEventStatusFailed).
			Default(OutboxEventStatusPending).
			Comment("投递状态机：pending / sent / failed"),
		field.Time("published_at").
			Optional().
			Nillable().
			Comment("relay 成功投递到 JetStream 的时间，空表示尚未投递"),
		field.Int("attempts").
			Default(0).
			Comment("已尝试投递次数，超过 outbox.max_attempts 置为 failed"),
		field.Int("version").
			Default(0).
			Comment("投递版本号，relay 每次成功投递 +1，供下游乐观并发检查"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("事件记账时间"),
	}
}

// Indexes 声明 pending 检索索引：relay 用 (status, created_at) 拉取待投递事件
func (OutboxEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
	}
}