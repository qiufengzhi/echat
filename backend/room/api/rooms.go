package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"echat-backend/authn"
	"echat-backend/logging"
	"echat-backend/transport"
)

// activeRoomRow 活跃频道列表返回的房间行
type activeRoomRow struct {
	// RoomCode 房间短码
	RoomCode string `json:"room_code"`
	// Status 房间状态（活跃房间恒为 active）
	Status string `json:"status"`
	// HostID 房主用户 id
	HostID string `json:"host_id"`
	// HostUsername 房主昵称
	HostUsername string `json:"host_username"`
	// ActiveMembers 当前在线成员数（Redis 读模型）
	ActiveMembers int64 `json:"active_members"`
}

// listRooms GET /api/v1/rooms 活跃频道列表：active_rooms 集合 + rooms 元数据 + Redis 成员数
func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	codes, err := h.rdb.SMembers(ctx, "active_rooms").Result()
	if err != nil {
		return dbError(err)
	}
	if len(codes) == 0 {
		transport.WriteJSON(w, http.StatusOK, map[string]any{"rooms": []activeRoomRow{}})
		return nil
	}

	rows, err := h.pool.Query(ctx,
		`SELECT r.room_code, r.status, r.host_id, COALESCE(u.username, '') AS host_username
		   FROM rooms r
		   LEFT JOIN users u ON u.id = r.host_id
		  WHERE r.room_code = ANY($1) AND r.status = 'active'
		  ORDER BY r.updated_at DESC`, codes)
	if err != nil {
		return dbError(err)
	}
	defer rows.Close()

	out := make([]activeRoomRow, 0, len(codes))
	for rows.Next() {
		var row activeRoomRow
		if err := rows.Scan(&row.RoomCode, &row.Status, &row.HostID, &row.HostUsername); err != nil {
			return dbError(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return dbError(err)
	}

	// 成员数以 Redis 读模型批量补齐（pipeline 一次往返）
	pipe := h.rdb.Pipeline()
	cmds := make(map[string]*redis.IntCmd, len(out))
	for i := range out {
		cmds[out[i].RoomCode] = pipe.HLen(ctx, "room:members:"+out[i].RoomCode)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return dbError(err)
	}
	for i := range out {
		if c, ok := cmds[out[i].RoomCode]; ok {
			out[i].ActiveMembers = c.Val()
		}
	}
	transport.WriteJSON(w, http.StatusOK, map[string]any{"rooms": out})
	return nil
}

// memberItem 房间详情成员项
type memberItem struct {
	// UserID 成员用户 id
	UserID string `json:"user_id"`
	// Username 成员昵称
	Username string `json:"username"`
}

// roomDetailRow 房间详情返回体
type roomDetailRow struct {
	// Exists 房间当前是否在线可加入（兼容旧 exists 检查端点语义）
	Exists bool `json:"exists"`
	// RoomCode 房间短码
	RoomCode string `json:"room_code"`
	// Status 房间状态：active / closed / archived 等
	Status string `json:"status"`
	// HostID 房主用户 id
	HostID string `json:"host_id"`
	// Members 当前成员快照（Redis 读模型）
	Members []memberItem `json:"members"`
}

// getRoom GET /api/v1/rooms/{code} 房间详情：rooms 事实行 + room:members:* 快照
func (h *Handler) getRoom(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	code := r.PathValue("code")
	if code == "" {
		return &transport.Error{Code: "VALIDATION_ERROR", Message: "缺少房间号", Status: http.StatusBadRequest}
	}

	var (
		status string
		hostID string
	)
	err := h.pool.QueryRow(ctx,
		`SELECT status, COALESCE(host_id::text, '') FROM rooms WHERE room_code = $1`, code).
		Scan(&status, &hostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return &transport.Error{Code: "ROOM_NOT_FOUND", Message: "房间不存在", Status: http.StatusNotFound}
	}
	if err != nil {
		return dbError(err)
	}

	inActive, err := h.rdb.SIsMember(ctx, "active_rooms", code).Result()
	if err != nil {
		return dbError(err)
	}
	details := roomDetailRow{
		Exists:   status == "active" && inActive,
		RoomCode: code,
		Status:   status,
		HostID:   hostID,
	}
	members, err := h.rdb.HGetAll(ctx, "room:members:"+code).Result()
	if err != nil {
		return dbError(err)
	}
	for uid, username := range members {
		details.Members = append(details.Members, memberItem{UserID: uid, Username: username})
	}
	transport.WriteJSON(w, http.StatusOK, details)
	return nil
}

// historyItem 我的历史单条记录
type historyItem struct {
	// RoomCode 房间短码
	RoomCode string `json:"room_code"`
	// RoomID 房间聚合根 id
	RoomID uuid.UUID `json:"room_id"`
	// Role 在场期间最后角色
	Role string `json:"role"`
	// JoinedAt 加入时间
	JoinedAt time.Time `json:"joined_at"`
	// LeftAt 离开时间（在场为 null）
	LeftAt *time.Time `json:"left_at"`
}

// myRooms GET /api/v1/me/rooms 我的房间历史：user_room_history 按用户倒序返回
func (h *Handler) myRooms(w http.ResponseWriter, r *http.Request) error {
	claims, ok := authn.ClaimsFrom(r.Context())
	if !ok {
		return authn.ErrAccessTokenInvalid
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT room_code, room_id, role, joined_at, left_at
		   FROM user_room_history
		  WHERE user_id = $1
		  ORDER BY joined_at DESC
		  LIMIT 50`, claims.SubjectUUID())
	if err != nil {
		return dbError(err)
	}
	defer rows.Close()

	out := make([]historyItem, 0)
	for rows.Next() {
		var (
			code   string
			roomID uuid.UUID
			role   string
			joined time.Time
			left   pgtype.Timestamptz
		)
		if err := rows.Scan(&code, &roomID, &role, &joined, &left); err != nil {
			return dbError(err)
		}
		it := historyItem{RoomCode: code, RoomID: roomID, Role: role, JoinedAt: joined}
		if left.Valid {
			t := left.Time
			it.LeftAt = &t
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	transport.WriteJSON(w, http.StatusOK, map[string]any{"rooms": out})
	return nil
}

// dbError 归一化 DB/Redis 查询失败为 500 内部错误（查询失败不外泄内部细节）
func dbError(err error) error {
	logging.L().Warnw("房间查询失败", "error", err)
	return &transport.Error{Code: "INTERNAL_ERROR", Message: "查询失败，请稍后再试", Status: http.StatusInternalServerError}
}
