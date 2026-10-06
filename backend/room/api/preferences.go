package api

import (
	"encoding/json"
	"net/http"

	"echat-backend/authn"
	"echat-backend/logging"
	"echat-backend/transport"
)

// roomJoinPreferences 进入房间默认行为，对应 users.preferences jsonb 设置包
// 键与前端 camelCase 字段在接口层映射为 snake_case；缺键时按 defaultRoomJoinPreferences 兜底
type roomJoinPreferences struct {
	MicOnByDefault     bool `json:"micOnByDefault"`
	SpeakerOnByDefault bool `json:"speakerOnByDefault"`
}

// defaultRoomJoinPreferences 新账号或偏好缺键时的默认行为：静音进房，但默认开启扬声器
func defaultRoomJoinPreferences() roomJoinPreferences {
	return roomJoinPreferences{MicOnByDefault: false, SpeakerOnByDefault: true}
}

// getPreferences GET /api/v1/me/preferences 读取当前账号进入房间默认行为
// 以默认值预置后再反序列化：缺键保留默认（扬声器开），字段存在则按账号实际选择覆盖
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

	pref := defaultRoomJoinPreferences()
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &pref); err != nil {
			logging.L().Warnw("解析偏好设置失败，按默认值兜底", "error", err)
			pref = defaultRoomJoinPreferences()
		}
	}
	transport.WriteJSON(w, http.StatusOK, pref)
	return nil
}

// putPreferences PUT /api/v1/me/preferences 整包覆盖保存当前账号进入房间默认行为
// 请求体缺键时保留默认值（扬声器开），避免客户端漏传键把默认开启的扬声器意外关掉
func (h *Handler) putPreferences(w http.ResponseWriter, r *http.Request) error {
	claims, ok := authn.ClaimsFrom(r.Context())
	if !ok {
		return authn.ErrAccessTokenInvalid
	}

	req := defaultRoomJoinPreferences()
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
