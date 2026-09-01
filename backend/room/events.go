package room

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// 房间域事件目录：接入已就绪的事务性 Outbox 骨干，供下游授权/热状态投影消费
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
)

// RoomEvent 一条待写入 outbox 的房间域事件
type RoomEvent struct {
	// Type 事件类型取值见上方 EventType* 枚举
	Type string
	// AggregateID 聚合根 id（room 内部 uuid），不对外展示
	AggregateID uuid.UUID
	// Subject 事件骨干 subject，按 room.{id}.{后缀} 编排
	Subject string
	// Payload 事件载荷 jsonb，键与取值按各事件约定
	Payload map[string]any
}

// roomSubject 拼接 room.{id}.{suffix} 形式的 JetStream subject
// aggID 房间聚合根 id，suffix 事件后缀（如 joined），返回完整 subject 串
func roomSubject(aggID uuid.UUID, suffix string) string {
	return "room." + aggID.String() + "." + suffix
}

// sqlExecer 抽象 pgx 事务与连接池共有的 Exec 方法，供事件写入在同事务或独立执行间切换
type sqlExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// recordEvent 写入一条 outbox 事件
// ctx 透传上下文，exe 执行器（事务对象 = 与事实同事务提交；连接池 = 独立提交），ev 待记账事件
// 返回值：写入失败则返回错误，调用方按「实时优先、DB 仅告警」策略丢日志不阻断信令
func recordEvent(ctx context.Context, exe sqlExecer, ev RoomEvent) error {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return err
	}
	// 刻意用 string 而非 []byte：jsonb 列需要文本编码，[]byte 会被 pgx 当作 bytea 二进制
	// created_at 显式 now()：ent 的 Default(time.Now) 只落在 ORM 层，DB 列无默认值
	_, err = exe.Exec(ctx,
		`INSERT INTO outbox_events (id, event_type, aggregate_id, subject, payload, status, created_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, 'pending', now())`,
		uuid.New(), ev.Type, ev.AggregateID, ev.Subject, string(payload))
	return err
}
