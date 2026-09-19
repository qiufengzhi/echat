package api

import (
	"encoding/json"
	"net/http"

	"echat-backend/authn"
	"echat-backend/logging"
	"echat-backend/transport"
)

// roomJoinPreferences 进入房间默认行为，对应 users.preferences jsonb 设置包
// 键与前端 camelCase 字段在接口层映射为 snake_case；缺键反序列化为零值 false（静音进房）
type roomJoinPreferences struct {
	MicOnByDefault     bool `json:"micOnByDefault"`
	SpeakerOnByDefault bool `json:"speakerOnByDefault"`
}

// getPreferences GET /api/v1/me/preferences 读取当前账号进入房间默认行为
// users 行缺失或偏好数据损坏时按「全关」兜底返回，保持 GET 幂等
func (h *Handler) getPreferences(w http.ResponseWriter, r *http.Request) error {
	claims, ok := authn.ClaimsFrom(r.Context())
	if !ok {
		return authn.ErrAccessTokenInvalid
	}

	var raw string
	err := h.pool.QueryRow(r.Context(),
		`SELECT preferences FROM users WHERE id = $1`, claims.SubjectUUID()).Scan(&raw)
	if err != nil {
		return dbError(err)
	}

	pref := roomJoinPreferences{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &pref); err != nil {
			logging.L().Warnw("解析偏好设置失败，按全关兜底", "error", err)
			pref = roomJoinPreferences{}
		}
	}
	transport.WriteJSON(w, http.StatusOK, pref)
	return nil
}

// putPreferences PUT /api/v1/me/preferences 整包覆盖保存当前账号进入房间默认行为
// 对象内缺键写为 false，避免残留上一次的键值
func (h *Handler) putPreferences(w http.ResponseWriter, r *http.Request) error {
	claims, ok := authn.ClaimsFrom(r.Context())
	if !ok {
		return authn.ErrAccessTokenInvalid
	}

	var req roomJoinPreferences
	if err := transport.ReadJSON(r, &req); err != nil {
		return err
	}

	val, err := json.Marshal(req)
	if err != nil {
		return &transport.Error{Code: "INTERNAL_ERROR", Message: "偏好设置编码失败，请稍后再试", Status: http.StatusInternalServerError}
	}
	// jsonb 参数以 text 传入并显式转型：pgx 会把 []byte 编码为 bytea，直接赋 jsonb 会类型不匹配
	if _, err := h.pool.Exec(r.Context(),
		`UPDATE users SET preferences = $2::jsonb, updated_at = now() WHERE id = $1`,
		claims.SubjectUUID(), string(val)); err != nil {
		return dbError(err)
	}
	transport.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}
