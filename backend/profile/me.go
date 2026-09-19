// me.go 资料编辑：PATCH /api/v1/me 更新当前账号资料（当前仅昵称）
package profile

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"echat-backend/authn"
	"echat-backend/transport"
)

// updateMeRequest 资料编辑请求体；缺省字段表示不更新
type updateMeRequest struct {
	DisplayName *string `json:"displayName"`
}

// displayNameMaxRunes 昵称长度上限（runes 计数，中英文一视同仁），与设计稿 20 字一致
const displayNameMaxRunes = 20

// updateMe PATCH /api/v1/me 更新昵称并返回最新用户概要，前端据此同步本地缓存
func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) error {
	claims, ok := authn.ClaimsFrom(r.Context())
	if !ok {
		return authn.ErrAccessTokenInvalid
	}

	var req updateMeRequest
	if err := transport.ReadJSON(r, &req); err != nil {
		return err
	}
	if req.DisplayName == nil {
		return authn.ErrBadRequest
	}
	name := strings.TrimSpace(*req.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > displayNameMaxRunes {
		return authn.ErrBadRequest
	}

	u, err := h.ent.User.UpdateOneID(claims.SubjectUUID()).SetDisplayName(name).Save(r.Context())
	if err != nil {
		return authn.ErrInternal
	}
	transport.WriteJSON(w, http.StatusOK, map[string]any{"user": authn.UserInfo{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		AvatarURL:   u.AvatarURL,
	}})
	return nil
}