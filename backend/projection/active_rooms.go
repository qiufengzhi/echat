package projection

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"echat-backend/room/events"
)

// decodePayload 解码事件 payload 到目标结构，失败返回错误（视为毒消息由上层处理）
func decodePayload(raw json.RawMessage, out any) error {
	return json.Unmarshal(raw, out)
}

// roomEventCtx 派生一次投影操作的超时上下文，避免慢 Redis/DB 阻塞消费循环
func roomEventCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 3*time.Second)
}

// ActiveRoomsProjector 活跃房间索引：room.created → SADD，room.closed → SREM
// 读模型 active_rooms 是一个 room_code 的 SET，供频道列表查询活跃房间
type ActiveRoomsProjector struct {
	// rdb Redis 客户端
	rdb *redis.Client
}

// NewActiveRoomsProjector 构造活跃房间投影器
// rdb Redis 客户端，需与在线热状态共用连接策略
func NewActiveRoomsProjector(rdb *redis.Client) *ActiveRoomsProjector {
	return &ActiveRoomsProjector{rdb: rdb}
}

// Handle 按事件类型更新 active_rooms SET
// 仅处理 room.created / room.closed，其余类型直接返回 nil
func (p *ActiveRoomsProjector) Handle(ctx context.Context, ev Event) error {
	var body struct {
		// RoomCode 房间短码
		RoomCode string `json:"room_code"`
	}
	if err := decodePayload(ev.Payload, &body); err != nil {
		return err
	}
	if body.RoomCode == "" {
		return nil
	}
	cctx, cancel := roomEventCtx(ctx)
	defer cancel()
	switch ev.Type {
	case events.EventTypeRoomCreated:
		return p.rdb.SAdd(cctx, "active_rooms", body.RoomCode).Err()
	case events.EventTypeRoomClosed:
		return p.rdb.SRem(cctx, "active_rooms", body.RoomCode).Err()
	default:
		return nil
	}
}
