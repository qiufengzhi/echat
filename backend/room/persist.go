package room

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// persistTimeout 定义事实写库的单次超时：实时信令路径不等待 DB，超时立即告警并继续广播
const persistTimeout = 3 * time.Second

// factErr 按「实时优先、DB 仅告警」记录事实写库失败，不阻断信令链路
// msg 失败上下文，roomID 关联房间短码，err 底层错误
func factErr(msg string, roomID string, err error) {
	logger.Warnw(msg, "roomID", roomID, "error", err)
}

// persistRoomJoin 把 join 的当前态写库并与领域事件同事务记账
// 按 room_code 判断事实行归属：不存在则并发安全地创建（发 room.created），已被关闭则复开为 active
// 成员在场行按 (room_id, user_id) 唯一键 upsert，重复加入重置 joined_at/left_at（left_at 置 NULL 表示在场）
// r 内存房间，c 加入成员；DB 失败仅告警，不阻塞内存广播与 SFU 信令
func persistRoomJoin(r *Room, c *Client) {
	if pool == nil {
		return
	}
	userID, err := uuid.Parse(c.UserID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil {
		factErr("开启房间加入事务失败", r.ID, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now()

	var (
		aggID uuid.UUID // 事务内确定的权威房间聚合根 id
		first bool      // 本条 join 是否建立/复开了房间事实（决定是否记 room.created）
	)
	var status string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM rooms WHERE room_code = $1`, r.ID).Scan(&aggID, &status)
	switch {
	case err == nil:
		if status == "closed" {
			// 复用被关闭的事实行，重置为 active，房主由当前首位成员担任
			if _, uerr := tx.Exec(ctx,
				`UPDATE rooms SET status = 'active', host_id = $1, updated_at = now() WHERE id = $2`,
				userID, aggID); uerr != nil {
				factErr("复开房间事实失败", r.ID, uerr)
				return
			}
			first = true
		}
	case errors.Is(err, pgx.ErrNoRows):
		// 以内存聚合根 id 作为事实行主键，与事件 subject 对齐；并发冲突交给 ON CONFLICT 收敛
		cand, perr := uuid.Parse(r.AggID)
		if perr != nil {
			return
		}
		tag, ierr := tx.Exec(ctx,
			`INSERT INTO rooms (id, room_code, host_id, status, created_at, updated_at)
			 VALUES ($1, $2, $3, 'active', now(), now())
			 ON CONFLICT (room_code) DO NOTHING`,
			cand, r.ID, userID)
		if ierr != nil {
			factErr("创建房间事实失败", r.ID, ierr)
			return
		}
		if tag.RowsAffected() == 1 {
			aggID = cand
			first = true
		} else {
			// 并发下已有实例建成，回查权威行以当前事务内数据为准
			if qerr := tx.QueryRow(ctx,
				`SELECT id FROM rooms WHERE room_code = $1`, r.ID).Scan(&aggID); qerr != nil {
				factErr("回查房间事实失败", r.ID, qerr)
				return
			}
			first = false
		}
	default:
		factErr("查询房间事实失败", r.ID, err)
		return
	}

	// 成员在场 upsert：冲突即重置昵称与加入时间，left_at 置 NULL 表示当前在场
	if _, err = tx.Exec(ctx,
		`INSERT INTO room_members (id, room_id, user_id, username, joined_at, left_at)
		 VALUES ($1, $2, $3, $4, $5, NULL)
		 ON CONFLICT (room_id, user_id) DO UPDATE
		   SET username = EXCLUDED.username, joined_at = EXCLUDED.joined_at, left_at = NULL`,
		uuid.New(), aggID, userID, c.Username, now); err != nil {
		factErr("写入成员在场失败", r.ID, err)
		return
	}

	// 领域事件与事实同事务记账，保证二态一致
	if first {
		if err = recordEvent(ctx, tx, RoomEvent{
			Type: EventTypeRoomCreated, AggregateID: aggID, Subject: roomSubject(aggID, "created"),
			Payload: map[string]any{"room_code": r.ID},
		}); err != nil {
			factErr("记账 room.created 失败", r.ID, err)
			return
		}
	}
	if err = recordEvent(ctx, tx, RoomEvent{
		Type: EventTypeRoomJoined, AggregateID: aggID, Subject: roomSubject(aggID, "joined"),
		Payload: map[string]any{"room_code": r.ID, "user_id": c.UserID, "username": c.Username},
	}); err != nil {
		factErr("记账 room.joined 失败", r.ID, err)
		return
	}

	if err = tx.Commit(ctx); err != nil {
		factErr("提交房间加入事实失败", r.ID, err)
		return
	}

	// 权威聚合根 id 与内存不一致时同步，保证 join/leave/ai 事件 subject 对齐
	// 复开已关闭事实行或并发回查会把事务内权威 id 收敛为 DB 行 id，内存 fresh 值需跟随
	if aggStr := aggID.String(); aggStr != r.AggID {
		r.Lock.Lock()
		r.AggID = aggStr
		r.Lock.Unlock()
	}
}

// persistRoomLeave 把 leave/断线/房主交接/房间清空的当前态写库并与领域事件同事务记账
// r 内存房间，c 离开成员，wasHost 是否为原房主，nextHostID 新房主（原房主离开且有值时生效），shouldDeleteRoom 房间是否清空，reason 离开来源（leave/disconnect/kicked）
// 交接会更新事实行 host_id 并记 host_transferred；清空腹状态置 closed 并归档所有在场成员
func persistRoomLeave(r *Room, c *Client, wasHost bool, nextHostID string, shouldDeleteRoom bool, reason string) {
	if pool == nil {
		return
	}
	userID, err := uuid.Parse(c.UserID)
	if err != nil {
		return
	}
	aggID, err := uuid.Parse(r.AggID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil {
		factErr("开启房间离开事务失败", r.ID, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now()

	// 离开者在场标记为已离开
	if _, err = tx.Exec(ctx,
		`UPDATE room_members SET left_at = $1 WHERE room_id = $2 AND user_id = $3 AND left_at IS NULL`,
		now, aggID, userID); err != nil {
		factErr("标记成员离开失败", r.ID, err)
		return
	}

	// 成员离开事件：reason 区分主动离开 / 断线 / 封禁踢下线
	if err = recordEvent(ctx, tx, RoomEvent{
		Type: EventTypeRoomLeft, AggregateID: aggID, Subject: roomSubject(aggID, "left"),
		Payload: map[string]any{"room_code": r.ID, "user_id": c.UserID, "reason": reason},
	}); err != nil {
		factErr("记账 room.left 失败", r.ID, err)
		return
	}

	// 原房主离开：更新事实行房主并记交接事件
	if wasHost && nextHostID != "" {
		nextUID, perr := uuid.Parse(nextHostID)
		if perr == nil {
			if _, err = tx.Exec(ctx,
				`UPDATE rooms SET host_id = $1, updated_at = now() WHERE id = $2`,
				nextUID, aggID); err != nil {
				factErr("更新房主事实失败", r.ID, err)
				return
			}
			if err = recordEvent(ctx, tx, RoomEvent{
				Type: EventTypeRoomHostTransferred, AggregateID: aggID, Subject: roomSubject(aggID, "host_transferred"),
				Payload: map[string]any{"room_code": r.ID, "from_user_id": c.UserID, "to_user_id": nextHostID},
			}); err != nil {
				factErr("记账 room.host_transferred 失败", r.ID, err)
				return
			}
		}
	}

	// 房间清空：归档所有在场成员并关闭房间事实行
	if shouldDeleteRoom {
		if _, err = tx.Exec(ctx,
			`UPDATE room_members SET left_at = $1 WHERE room_id = $2 AND left_at IS NULL`,
			now, aggID); err != nil {
			factErr("归档房间成员失败", r.ID, err)
			return
		}
		if _, err = tx.Exec(ctx,
			`UPDATE rooms SET status = 'closed', closed_at = $1, updated_at = $1 WHERE id = $2`,
			now, aggID); err != nil {
			factErr("关闭房间事实失败", r.ID, err)
			return
		}
		if err = recordEvent(ctx, tx, RoomEvent{
			Type: EventTypeRoomClosed, AggregateID: aggID, Subject: roomSubject(aggID, "closed"),
			Payload: map[string]any{"room_code": r.ID},
		}); err != nil {
			factErr("记账 room.closed 失败", r.ID, err)
			return
		}
	}

	if err = tx.Commit(ctx); err != nil {
		factErr("提交房间离开事实失败", r.ID, err)
	}
}

// persistAiToggle 记录 AI 语音助手开关事件，AI 状态本体仍由内存状态机持有，不落事实字段
// roomID 房间短码，state 开关后的状态描述（online / offline）
// 房间不在内存中（异常）或无聚合根 id 时跳过
func persistAiToggle(roomID string, state string) {
	if pool == nil {
		return
	}
	roomLock.RLock()
	signalRoom, ok := allSignalRooms[roomID]
	roomLock.RUnlock()
	if !ok || signalRoom == nil || signalRoom.AggID == "" {
		return
	}
	aggID, err := uuid.Parse(signalRoom.AggID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if err = recordEvent(ctx, pool, RoomEvent{
		Type: EventTypeRoomAiToggled, AggregateID: aggID, Subject: roomSubject(aggID, "ai_toggled"),
		Payload: map[string]any{"room_code": roomID, "state": state},
	}); err != nil {
		factErr("记账 room.ai_toggled 失败", roomID, err)
	}
}
