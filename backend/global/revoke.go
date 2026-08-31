package global

import "github.com/google/uuid"

// UserRevokedEvent 描述一次会话吊销，实时层据此踢掉对应 WebSocket 连接（封禁即下线）
// outbox 落库的 user.* 事件是持久真相，本通道是进程内即时风扇，事件丢失可由下游投影兜底
type UserRevokedEvent struct {
	// UserID 被吊销会话所属用户
	UserID uuid.UUID
	// KeepSessionID 保留不踢的会话（改密时的当前会话）；Nil 表示该用户全部会话都踢
	KeepSessionID uuid.UUID
	// Reason 吊销原因：logout-all / password.reset / password.change / suspended
	Reason string
}

// UserRevokedCh 会话吊销的进程内事件通道，room 包消费后按连接绑定身份踢下线
var UserRevokedCh = make(chan UserRevokedEvent, 64)