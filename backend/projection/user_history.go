package projection

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"echat-backend/room/events"
)

// userHistoryRoleListener 加入事件落库的默认在场角色，后续 host_transferred 校正
const userHistoryRoleListener = "listener"

// UserHistoryProjector 用户房间历史：joined/left/host_transferred → 幂等 upsert user_room_history
// 读模型按 (user_id, room_id) 唯一，一次进入一场；「我的历史」与后续角色复盘据此查询
type UserHistoryProjector struct {
	// pool pgx 连接池，直写 user_room_history
	pool *pgxpool.Pool
}

// NewUserHistoryProjector 构造用户房间历史投影器
// pool 数据库连接池，与业务写库共用
func NewUserHistoryProjector(pool *pgxpool.Pool) *UserHistoryProjector {
	return &UserHistoryProjector{pool: pool}
}

// Handle 按事件类型更新 user_room_history
// room.joined → upsert 一条在场记录；room.left → 置 left_at；room.host_transferred → 校正在场角色
func (p *UserHistoryProjector) Handle(ctx context.Context, ev Event) error {
	when, err := time.Parse(time.RFC3339Nano, ev.OccurredAt)
	if err != nil {
		// 时间缺失时以当前时间近似，避免整条事件因单字段畸形而重投
		when = time.Now()
	}
	roomID, err := uuid.Parse(ev.AggregateID)
	if err != nil {
		return err
	}
	cctx, cancel := roomEventCtx(ctx)
	defer cancel()

	switch ev.Type {
	case events.EventTypeRoomJoined:
		return p.handleJoined(cctx, ev.Payload, roomID, when)
	case events.EventTypeRoomLeft:
		return p.handleLeft(cctx, ev.Payload, roomID, when)
	case events.EventTypeRoomHostTransferred:
		return p.handleHostTransferred(cctx, ev.Payload, roomID)
	default:
		return nil
	}
}

// handleJoined 加入即 upsert 在场记录：left_at 置空表示在场，角色保持既有（默认 listener）
func (p *UserHistoryProjector) handleJoined(ctx context.Context, raw json.RawMessage, roomID uuid.UUID, when time.Time) error {
	var body struct {
		// RoomCode 房间短码（冗余，查询免 join）
		RoomCode string `json:"room_code"`
		// UserID 加入成员
		UserID string `json:"user_id"`
	}
	if err := decodePayload(raw, &body); err != nil {
		return err
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		return nil
	}
	_, err = p.pool.Exec(ctx,
		`INSERT INTO user_room_history (id, user_id, room_id, room_code, joined_at, left_at, role)
		 VALUES ($1, $2, $3, $4, $5, NULL, $6)
		 ON CONFLICT (user_id, room_id) DO UPDATE
		   SET room_code = EXCLUDED.room_code, joined_at = EXCLUDED.joined_at, left_at = NULL`,
		uuid.New(), userID, roomID, body.RoomCode, when, userHistoryRoleListener)
	return err
}

// handleLeft 置离开时间：仅当天在场记录生效，重复 left 事件幂等跳过
func (p *UserHistoryProjector) handleLeft(ctx context.Context, raw json.RawMessage, roomID uuid.UUID, when time.Time) error {
	var body struct {
		// UserID 离开成员
		UserID string `json:"user_id"`
	}
	if err := decodePayload(raw, &body); err != nil {
		return err
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		return nil
	}
	_, err = p.pool.Exec(ctx,
		`UPDATE user_room_history SET left_at = $1 WHERE user_id = $2 AND room_id = $3 AND left_at IS NULL`,
		when, userID, roomID)
	return err
}

// handleHostTransferred 校正在场角色：原房主转 listener，新房主转 host
func (p *UserHistoryProjector) handleHostTransferred(ctx context.Context, raw json.RawMessage, roomID uuid.UUID) error {
	var body struct {
		// FromUserID 原房主
		FromUserID string `json:"from_user_id"`
		// ToUserID 新房主
		ToUserID string `json:"to_user_id"`
	}
	if err := decodePayload(raw, &body); err != nil {
		return err
	}
	fromID, ferr := uuid.Parse(body.FromUserID)
	toID, terr := uuid.Parse(body.ToUserID)
	if ferr != nil && terr != nil {
		return nil
	}
	// 角色校正限定在场记录（left_at IS NULL），已离场的用户角色不再覆盖
	if ferr == nil {
		if _, err := p.pool.Exec(ctx,
			`UPDATE user_room_history SET role = $1 WHERE user_id = $2 AND room_id = $3 AND left_at IS NULL`,
			userHistoryRoleListener, fromID, roomID); err != nil {
			return err
		}
	}
	if terr == nil {
		if _, err := p.pool.Exec(ctx,
			`UPDATE user_room_history SET role = $1 WHERE user_id = $2 AND room_id = $3 AND left_at IS NULL`,
			"host", toID, roomID); err != nil {
			return err
		}
	}
	return nil
}
