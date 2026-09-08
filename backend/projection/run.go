package projection

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"echat-backend/logging"
	"echat-backend/room/events"
)

// durableRoomProjection durable consumer 名：房间读模型投影的断点游标坐标
const durableRoomProjection = "projection-rooms"

// StartRoomReadModel 在后台启动房间读模型投影（active_rooms / room:members:* / user_room_history）
// 任一投影器暂不可用会阻塞对应事件 Ack 并自动重投，收敛后继续
// ctx 用于中断，rdb Redis 客户端，pool 数据库连接池，natsURL 与 stream 与 outbox relay 对齐
func StartRoomReadModel(ctx context.Context, rdb *redis.Client, pool *pgxpool.Pool, natsURL, stream string) {
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

	if err := router.Run(ctx); err != nil {
		logging.L().Warnw("房间读模型投影退出", "durable", durableRoomProjection, "error", err)
	}
}
