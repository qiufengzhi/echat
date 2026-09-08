// Package persist 房间事实落库层：实现 aggregate.FactStore / aggregate.EventRecorder 端口
//
// 分层：infrastructure——把聚合收敛后的当前态与领域事件以「同事务」方式写入 Postgres
// 事务性 Outbox 骨架：join/leave/交接/清空 在单个事务内同时落 rooms/room_members 与 outbox_events
// 失败按「实时优先、DB 仅告警」策略返回错误，由调用方（signaling）记录日志不阻断信令主链路
package persist

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"echat-backend/logging"
	"echat-backend/room/aggregate"
	"echat-backend/room/events"
)

// logger persist 包的具名日志器
var logger = logging.New("persist")

// persistTimeout 定义事实写库的单次超时：实时信令路径不等待 DB，超时立即告警并继续广播
const persistTimeout = 3 * time.Second

// execer 抽象 pgx 事务与连接池共有的 Exec，供事实与事件写入在同事务或独立执行间切换
type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// Persister 房间事实落库器：持有 pgx 连接池，实现 FactStore 与 EventRecorder
type Persister struct {
	// pool pgx 连接池，事务与独立写共用
	pool *pgxpool.Pool
}

// New 构造事实落库器
// pool 由 App 注入的连接池，与事务性 outbox relay 共用
func New(pool *pgxpool.Pool) *Persister {
	return &Persister{pool: pool}
}

// factErr 按「实时优先、DB 仅告警」记录事实写库失败
// msg 失败上下文，roomID 关联房间短码，err 底层错误
func factErr(msg string, roomID string, err error) {
	logger.Warnw(msg, "roomID", roomID, "error", err)
}

// JoinRoom 实现 aggregate.FactStore：把 join 的当前态写库并与领域事件同事务记账
// 按 room_code 判断事实行归属：不存在则并发安全创建（记 room.created），closed 则复开为 active
// 成员在场行按 (room_id, user_id) 唯一键 upsert，重复加入重置 joined_at/left_at
// room 内存聚合房间，member 加入成员；写库后若权威聚合根 id 与内存不一致则收敛回写
func (p *Persister) JoinRoom(ctx context.Context, room *aggregate.Room, member aggregate.Session) error {
	ctx, cancel := context.WithTimeout(ctx, persistTimeout)
	defer cancel()

	userID, err := uuid.Parse(member.SessionUserID())
	if err != nil {
		return nil
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		factErr("开启房间加入事务失败", room.ID, err)
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now()

	var (
		aggID uuid.UUID // 事务内确定的权威房间聚合根 id
		first bool      // 本条 join 是否建立/复开了房间事实（决定是否记 room.created）
	)
	var status string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM rooms WHERE room_code = $1`, room.ID).Scan(&aggID, &status)
	switch {
	case err == nil:
		if status == "closed" {
			// 复用被关闭的事实行，重置为 active，房主由当前首位成员担任
			if _, uerr := tx.Exec(ctx,
				`UPDATE rooms SET status = 'active', host_id = $1, updated_at = now() WHERE id = $2`,
				userID, aggID); uerr != nil {
				factErr("复开房间事实失败", room.ID, uerr)
				return uerr
			}
			first = true
		}
	case errors.Is(err, pgx.ErrNoRows):
		// 以内存聚合根 id 作为事实行主键，与事件 subject 对齐；并发冲突交给 ON CONFLICT 收敛
		cand, perr := uuid.Parse(room.AggID)
		if perr != nil {
			return nil
		}
		tag, ierr := tx.Exec(ctx,
			`INSERT INTO rooms (id, room_code, host_id, status, created_at, updated_at)
			 VALUES ($1, $2, $3, 'active', now(), now())
			 ON CONFLICT (room_code) DO NOTHING`,
			cand, room.ID, userID)
		if ierr != nil {
			factErr("创建房间事实失败", room.ID, ierr)
			return ierr
		}
		if tag.RowsAffected() == 1 {
			aggID = cand
			first = true
		} else {
			// 并发下已有实例建成，回查权威行以当前事务内数据为准
			if qerr := tx.QueryRow(ctx,
				`SELECT id FROM rooms WHERE room_code = $1`, room.ID).Scan(&aggID); qerr != nil {
				factErr("回查房间事实失败", room.ID, qerr)
				return qerr
			}
			first = false
		}
	default:
		factErr("查询房间事实失败", room.ID, err)
		return err
	}

	// 成员在场 upsert：冲突即重置昵称与加入时间，left_at 置 NULL 表示当前在场
	if _, err = tx.Exec(ctx,
		`INSERT INTO room_members (id, room_id, user_id, username, joined_at, left_at)
		 VALUES ($1, $2, $3, $4, $5, NULL)
		 ON CONFLICT (room_id, user_id) DO UPDATE
		   SET username = EXCLUDED.username, joined_at = EXCLUDED.joined_at, left_at = NULL`,
		uuid.New(), aggID, userID, member.SessionUsername(), now); err != nil {
		factErr("写入成员在场失败", room.ID, err)
		return err
	}

	// 领域事件与事实同事务记账，保证二态一致
	if first {
		if err = insertEvent(ctx, tx, events.RoomEvent{
			Type: events.EventTypeRoomCreated, AggregateID: aggID, Subject: events.Subject(aggID, "created"),
			Payload: map[string]any{"room_code": room.ID},
		}); err != nil {
			factErr("记账 room.created 失败", room.ID, err)
			return err
		}
	}
	if err = insertEvent(ctx, tx, events.RoomEvent{
		Type: events.EventTypeRoomJoined, AggregateID: aggID, Subject: events.Subject(aggID, "joined"),
		Payload: map[string]any{"room_code": room.ID, "user_id": member.SessionUserID(), "username": member.SessionUsername()},
	}); err != nil {
		factErr("记账 room.joined 失败", room.ID, err)
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		factErr("提交房间加入事实失败", room.ID, err)
		return err
	}

	// 权威聚合根 id 与内存不一致时同步，保证 join/leave/ai 事件 subject 对齐
	// 复开已关闭事实行或并发回查会把事务内权威 id 收敛为 DB 行 id，内存 fresh 值需跟随
	if aggStr := aggID.String(); aggStr != room.AggID {
		room.Lock.Lock()
		room.AggID = aggStr
		room.Lock.Unlock()
	}
	return nil
}

// LeaveRoom 实现 aggregate.FactStore：把 leave/交接/清空的当前态写库并与领域事件同事务记账
// room 内存聚合房间，leaverUserID 离开成员身份，wasHost 是否原房主，nextHostID 新房主，shouldDelete 房间是否清空，reason 离开来源（leave/disconnect/kicked）
func (p *Persister) LeaveRoom(ctx context.Context, room *aggregate.Room, leaverUserID string, wasHost bool, nextHostID string, shouldDelete bool, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, persistTimeout)
	defer cancel()

	userID, err := uuid.Parse(leaverUserID)
	if err != nil {
		return nil
	}
	aggID, err := uuid.Parse(room.AggID)
	if err != nil {
		return nil
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		factErr("开启房间离开事务失败", room.ID, err)
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now()

	// 离开者在场标记为已离开
	if _, err = tx.Exec(ctx,
		`UPDATE room_members SET left_at = $1 WHERE room_id = $2 AND user_id = $3 AND left_at IS NULL`,
		now, aggID, userID); err != nil {
		factErr("标记成员离开失败", room.ID, err)
		return err
	}

	// 成员离开事件：reason 区分主动离开 / 断线 / 封禁踢下线
	if err = insertEvent(ctx, tx, events.RoomEvent{
		Type: events.EventTypeRoomLeft, AggregateID: aggID, Subject: events.Subject(aggID, "left"),
		Payload: map[string]any{"room_code": room.ID, "user_id": leaverUserID, "reason": reason},
	}); err != nil {
		factErr("记账 room.left 失败", room.ID, err)
		return err
	}

	// 原房主离开：更新事实行房主并记交接事件
	if wasHost && nextHostID != "" {
		nextUID, perr := uuid.Parse(nextHostID)
		if perr == nil {
			if _, err = tx.Exec(ctx,
				`UPDATE rooms SET host_id = $1, updated_at = now() WHERE id = $2`,
				nextUID, aggID); err != nil {
				factErr("更新房主事实失败", room.ID, err)
				return err
			}
			if err = insertEvent(ctx, tx, events.RoomEvent{
				Type: events.EventTypeRoomHostTransferred, AggregateID: aggID, Subject: events.Subject(aggID, "host_transferred"),
				Payload: map[string]any{"room_code": room.ID, "from_user_id": leaverUserID, "to_user_id": nextHostID},
			}); err != nil {
				factErr("记账 room.host_transferred 失败", room.ID, err)
				return err
			}
		}
	}

	// 房间清空：归档所有在场成员并关闭房间事实行
	if shouldDelete {
		if _, err = tx.Exec(ctx,
			`UPDATE room_members SET left_at = $1 WHERE room_id = $2 AND left_at IS NULL`,
			now, aggID); err != nil {
			factErr("归档房间成员失败", room.ID, err)
			return err
		}
		if _, err = tx.Exec(ctx,
			`UPDATE rooms SET status = 'closed', closed_at = $1, updated_at = $1 WHERE id = $2`,
			now, aggID); err != nil {
			factErr("关闭房间事实失败", room.ID, err)
			return err
		}
		if err = insertEvent(ctx, tx, events.RoomEvent{
			Type: events.EventTypeRoomClosed, AggregateID: aggID, Subject: events.Subject(aggID, "closed"),
			Payload: map[string]any{"room_code": room.ID},
		}); err != nil {
			factErr("记账 room.closed 失败", room.ID, err)
			return err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		factErr("提交房间离开事实失败", room.ID, err)
		return err
	}
	return nil
}

// Record 实现 aggregate.EventRecorder：把一条孤立领域事件写入 outbox（不落在事实事务内）
// ev 由调用方组装好类型/聚合根/subject/payload，供举手、上麦管理、AI 开关等即时记账复用
func (p *Persister) Record(ctx context.Context, ev events.RoomEvent) error {
	ctx, cancel := context.WithTimeout(ctx, persistTimeout)
	defer cancel()
	if err := insertEvent(ctx, p.pool, ev); err != nil {
		factErr("记账成员管理事件失败", roomCodeOf(ev.Payload), err)
		return err
	}
	return nil
}

// insertEvent 写入一条 outbox 事件
// ctx 透传上下文，exe 执行器（事务对象 = 与事实同事务提交；连接池 = 独立提交），ev 待记账事件
func insertEvent(ctx context.Context, exe execer, ev events.RoomEvent) error {
	payload, err := marshalPayload(ev.Payload)
	if err != nil {
		return err
	}
	// 刻意用 string 而非 []byte：jsonb 列需要文本编码，[]byte 会被 pgx 当作 bytea 二进制
	// created_at 显式 now()：ent 的 Default(time.Now) 只落在 ORM 层，DB 列无默认值
	_, err = exe.Exec(ctx,
		`INSERT INTO outbox_events (id, event_type, aggregate_id, subject, payload, status, created_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, 'pending', now())`,
		uuid.New(), ev.Type, ev.AggregateID, ev.Subject, payload)
	return err
}
