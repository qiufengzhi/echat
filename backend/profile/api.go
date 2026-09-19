// Package profile 用户资料域：资料编辑与头像上传
//
// 头像走「本地磁盘落盘 + URL 引用」：前端裁剪压到 ≤512px 后 multipart 上传，后端
// 校验魔数与大小存盘，avatar_url 只存相对 URL，经 /uploads 静态直出。日后接对象
// 存储只需替换存储这一层，接口与 URL 形态不变。
package profile

import (
	"fmt"
	"net/http"
	"os"

	"echat-backend/authn"
	"echat-backend/ent"
	"echat-backend/transport"
)

// Handler 资料域 HTTP 处理器
type Handler struct {
	// ent Ent 客户端，读写 users 资料字段
	ent *ent.Client
	// dir 上传文件落盘目录
	dir string
	// auth 认证域处理器，提供 AccessRequired 中间件与 claims 解析
	auth *authn.Handler
}

// NewHandler 构造资料域处理器；uploadsDir 不存在时自动创建，返回错误交装配层处理
func NewHandler(client *ent.Client, uploadsDir string, auth *authn.Handler) (*Handler, error) {
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建上传目录 %s: %w", uploadsDir, err)
	}
	return &Handler{ent: client, dir: uploadsDir, auth: auth}, nil
}

// Register 资料域路由注册：头像上传与资料编辑走 AccessRequired；静态头像直出不鉴权
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/me/avatar", transport.Adapt(h.auth.AccessRequired(h.uploadAvatar)))
	mux.HandleFunc("PATCH /api/v1/me", transport.Adapt(h.auth.AccessRequired(h.updateMe)))
	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(h.dir))))
}