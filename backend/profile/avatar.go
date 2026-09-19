// avatar.go 头像上传：multipart 接收 → 魔数/大小校验 → 本地磁盘落盘 → avatar_url 换新
package profile

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"echat-backend/authn"
	"echat-backend/ent"
	"echat-backend/transport"

	"github.com/google/uuid"
)

// 头像上传大小约束：上限 5 MiB、下限 1 KiB 挡空文件；前端已把图压到 ≤512px
const (
	maxAvatarBytes = 5 << 20 // 5 MiB
	minAvatarBytes = 1 << 10 // 1 KiB
)

// avatarExt MIME → 扩展名映射；http.DetectContentType 读前 512 字节判魔数，只放行这三类
var avatarExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// uploadAvatar POST /api/v1/me/avatar 上传并替换当前账号头像
// multipart 字段名 file；先落盘再改库，库成功前任何失败都回滚已写文件
func (h *Handler) uploadAvatar(w http.ResponseWriter, r *http.Request) error {
	claims, ok := authn.ClaimsFrom(r.Context())
	if !ok {
		return authn.ErrAccessTokenInvalid
	}
	userID := claims.SubjectUUID()

	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		return avatarError("图片上传失败")
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return avatarError("请选择要上传的图片")
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil || len(data) < minAvatarBytes || len(data) > maxAvatarBytes {
		return avatarError("图片不合法或超出大小限制")
	}
	ext, ok := avatarExt[http.DetectContentType(data)]
	if !ok {
		return avatarError("仅支持 JPG、PNG、WebP 图片")
	}

	// 先读旧头像，成功落库后再 best-effort 删旧文件，避免孤儿
	prev, err := h.ent.User.Get(r.Context(), userID)
	if err != nil {
		return authn.ErrInternal
	}

	name := uuid.NewString() + ext
	dst := filepath.Join(h.dir, name)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return authn.ErrInternal
	}
	avatarURL := "/uploads/" + name

	if _, err := h.ent.User.UpdateOneID(userID).SetAvatarURL(avatarURL).Save(r.Context()); err != nil {
		_ = os.Remove(dst) // 库没落成，删掉刚写入的文件保持一致
		return authn.ErrInternal
	}

	if prev.AvatarURL != nil {
		removeLocalUpload(*prev.AvatarURL, h.dir)
	}
	transport.WriteJSON(w, http.StatusOK, map[string]any{"user": avatarUserResponse(prev, &avatarURL)})
	return nil
}

// avatarError 头像上传的泛化 400 文案，不外泄具体文件系统细节
func avatarError(message string) *transport.Error {
	return &transport.Error{Code: "INVALID_IMAGE", Message: message, Status: http.StatusBadRequest}
}

// avatarUserResponse 组装资料响应体，昵称/用户名复用上传前的实体（头像更新不触及这两字段）
func avatarUserResponse(u *ent.User, avatarURL *string) authn.UserInfo {
	return authn.UserInfo{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		AvatarURL:   avatarURL,
	}
}

// removeLocalUpload 删除本地上传的旧头像文件
// 只接受 /uploads/ 前缀且文件名不含路径分隔符的 URL，防止误删目录内其他文件
func removeLocalUpload(url, dir string) {
	name := strings.TrimPrefix(url, "/uploads/")
	if name == url || name == "" || name[0] == '.' || name != filepath.Base(name) {
		return
	}
	_ = os.Remove(filepath.Join(dir, name))
}