package transport

import (
	"encoding/json"
	"net/http"
)

// Error 可对外序列化的领域错误：任何领域返回该类型，统一由 WriteError 写成包络
type Error struct {
	// Code 错误码，前端据此分支处理
	Code string
	// Message 面向用户的泛化文案
	Message string
	// Status HTTP 状态码，0 表示 500
	Status int
}

// Error 实现 error 接口，返回用户可见文案
func (e *Error) Error() string { return e.Message }

// errorEnvelope 对外错误响应包络，统一包一层 error 字段
type errorEnvelope struct {
	// Error 错误主体，含 code 与 message
	Error errorBody `json:"error"`
}

// errorBody 错误主体
type errorBody struct {
	// Code 错误码，前端可据此分支处理
	Code string `json:"code"`
	// Message 面向用户的泛化文案
	Message string `json:"message"`
}

// WriteJSON 以 application/json 写成功响应
// w 响应写入器，status HTTP 状态码，v 待编码的响应体（nil 时写 null）
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError 把错误写为统一错误包络；nil 或非 *Error 按内部错误处理
// w 响应写入器，err 领域错误；断言 *Error 失败时兜底为内部错误，避免泄露内部细节
func WriteError(w http.ResponseWriter, err error) {
	ae, ok := err.(*Error)
	if !ok || ae == nil {
		ae = &Error{Code: "INTERNAL_ERROR", Message: "服务器开小差了，请稍后再试", Status: http.StatusInternalServerError}
	}
	if ae.Status == 0 {
		ae.Status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{Code: ae.Code, Message: ae.Message}})
}

// ReadJSON 读取并解析 JSON 请求体到 out，限制 1 MiB，失败返回错误
// r 源请求，out 解码目标；超限或非法 JSON 统一映射为 400，避免超大请求体拖垮服务
func ReadJSON(r *http.Request, out any) error {
	const maxBody = 1 << 20
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		return &Error{Code: "VALIDATION_ERROR", Message: "请求参数不合法", Status: http.StatusBadRequest}
	}
	return nil
}
