package authn

import (
	"context"

	"echat-backend/ent"

	"github.com/google/uuid"
)

// 用户域事件目录：与蓝图 §2.4 事件表一致，供 outbox 记账与下游消费对齐
const (
	// EventTypeUserRegistered 注册成功
	EventTypeUserRegistered = "user.registered"
	// EventTypeUserVerified 邮箱验证通过（注册激活）
	EventTypeUserVerified = "user.verified"
	// EventTypeUserEmailBound 本地账号成功绑定邮箱
	EventTypeUserEmailBound = "user.email.bound"
	// EventTypeUserLoginSucceeded 登录成功
	EventTypeUserLoginSucceeded = "user.login.succeeded"
	// EventTypeUserLoginFailed 登录失败
	EventTypeUserLoginFailed = "user.login.failed"
	// EventTypeUserPasswordChanged 改密/重置
	EventTypeUserPasswordChanged = "user.password.changed"
	// EventTypeUserSuspended 管理员封禁
	EventTypeUserSuspended = "user.suspended"
)

// Event 一条待写入 outbox 的领域事件
type Event struct {
	// Type 事件类型取值见上方 EventType* 枚举
	Type string
	// AggregateID 聚合根 id（此处为 user_id）
	AggregateID uuid.UUID
	// Subject 事件骨干 subject，按 user.{id}.{事件} 编排
	Subject string
	// Payload 事件载荷 jsonb，可空
	Payload map[string]any
}

// userSubject 拼接 user.{id}.{suffix} 形式的 JetStream subject
// userID 归属用户，suffix 事件后缀（如 registered），返回完整 subject 串
func userSubject(userID uuid.UUID, suffix string) string {
	return "user." + userID.String() + "." + suffix
}

// recordEvent 在既有事务内写入一条 outbox 事件，与业务数据同事务提交
// ctx 透传事务上下文，tx 进行中的 ent 事务，ev 待记账事件
func recordEvent(ctx context.Context, tx *ent.Tx, ev Event) error {
	_, err := tx.OutboxEvent.Create().
		SetID(uuid.New()).
		SetEventType(ev.Type).
		SetAggregateID(ev.AggregateID).
		SetSubject(ev.Subject).
		SetPayload(ev.Payload).
		Save(ctx)
	return err
}
