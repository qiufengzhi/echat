package projection

import (
	"context"

	"github.com/redis/go-redis/v9"

	"echat-backend/room/events"
)

// RoomMembersProjector 房间成员快照：room.joined → HSET，room.left → HDEL
// key = room:members:{room_code}，field = user_id，value = username
// 供房间详情/成员席位读模型即时查询，随加入/离开幂等收敛
type RoomMembersProjector struct {
	// rdb Redis 客户端
	rdb *redis.Client
}

// NewRoomMembersProjector 构造房间成员快照投影器
// rdb Redis 客户端
func NewRoomMembersProjector(rdb *redis.Client) *RoomMembersProjector {
	return &RoomMembersProjector{rdb: rdb}
}

// Handle 按事件类型维护 room:members:{code} HASH
// 仅处理 room.joined / room.left，其余类型直接返回 nil
// 写入经 Lua 版本守卫（LWW by seq）条件应用：早于水位的事件晚到被拒绝，不误删已重新加入的成员
func (p *RoomMembersProjector) Handle(ctx context.Context, ev Event) error {
	cctx, cancel := roomEventCtx(ctx)
	defer cancel()

	var base struct {
		// RoomCode 房间短码
		RoomCode string `json:"room_code"`
		// UserID 成员用户 id
		UserID string `json:"user_id"`
	}
	if err := decodePayload(ev.Payload, &base); err != nil {
		return err
	}
	if base.RoomCode == "" || base.UserID == "" {
		return nil
	}

	op := ""
	username := ""
	switch ev.Type {
	case events.EventTypeRoomJoined:
		var body struct {
			// Username 成员昵称
			Username string `json:"username"`
		}
		if err := decodePayload(ev.Payload, &body); err != nil {
			return err
		}
		op = "join"
		username = body.Username
	case events.EventTypeRoomLeft:
		op = "left"
	default:
		return nil
	}

	// 读模型键与水位键分属两个命名空间，独立前缀隔离任意客户端短码构成的歧义
	// 脚本返回 0 表示事件早于已应用水位、属乱序重投，读模型已是最新状态，按成功处理
	_, err := rosterApplyScript.Run(cctx, p.rdb,
		[]string{"room:members:" + base.RoomCode, rosterGuardPrefix + base.RoomCode},
		base.UserID, ev.Seq, op, username).Int()
	return err
}
