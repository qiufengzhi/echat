package persist

import "encoding/json"

// marshalPayload 把事件载荷编码为 jsonb 可接受的文本串
// 空载荷按空对象处理，避免写出 JSON null 后无法写入 jsonb 列
func marshalPayload(payload map[string]any) (string, error) {
	if len(payload) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// roomCodeOf 从事件载荷中取房间短码（可能缺失或非字符串，返回空串）
func roomCodeOf(payload map[string]any) string {
	if code, ok := payload["room_code"].(string); ok {
		return code
	}
	return ""
}
