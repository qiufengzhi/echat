package projection

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"echat-backend/authz"
	"echat-backend/logging"
	"echat-backend/room/events"
)

// durableRoomProjection durable consumer 名：房间读模型与授权对账投影的断点游标坐标
const durableRoomProjection = "projection-rooms"

// StartRoomReadModel 在后台启动房间事件投影（读模型 + 授权对账）
// 读模型：active_rooms / room:members:* / user_room_history
// 授权对账：az 非 nil 时注册 AuthzSyncProjector，消费角色事件兜底收敛 SpiceDB 漂移
// 任一投影器暂不可用会阻塞对应事件 Ack 并自动重投，收敛后继续
// ctx 用于中断，rdb Redis 客户端，pool 数据库连接池，natsURL 与 stream 与 outbox relay 对齐
func StartRoomReadModel(ctx context.Context, rdb *redis.Client, pool *pgxpool.Pool, natsURL, stream string, az *authz.Client) {
	router := NewRouter(Config{
		NatsURL: natsURL,
		Stream:  stream,
		Durable: durableRoomProjection,
		Batch:   16,
	})
	router.Handle(NewActiveRoomsProjector(rdb), events.EventTypeRoomCreated, events.EventTypeRoomClosed)
	router.Handle(NewRoomMembersProjector(rdb), events.EventTypeRoomJoined, events.EventTypeRoomLeft)
	router.Handle(NewUserHistoryProjector(pool),
		events.EventTypeRoomJoined, events.EventTypeRoomLeft, events.EventTypeRoomHostTransferred)
	if az != nil {
		router.Handle(NewAuthzSyncProjector(az),
			events.EventTypeRoomJoined,
			events.EventTypeRoomLeft,
			events.EventTypeRoomHostTransferred,
			events.EventTypeRoomMicApproved,
			events.EventTypeRoomMicKicked,
			events.EventTypeRoomMuted,
		)
	}

	if err := router.Run(ctx); err != nil {
		logging.L().Warnw("房间读模型投影退出", "durable", durableRoomProjection, "error", err)
	}
}
