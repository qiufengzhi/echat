package transport

import "net/http"

// HandlerFunc 业务处理器统一签名：成功写响应，失败返回 error 交给 Adapt 兜底
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Adapt 把 HandlerFunc 适配为标准 http.HandlerFunc
// 捕获 error 后走 WriteError 写统一错误包络，nil 错误不重复写响应
func Adapt(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, err)
		}
	}
}
