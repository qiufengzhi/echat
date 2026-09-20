package sfu

// aiCallReq AI 语音助手请求，用于启动 TTS 音频处理循环
type aiCallReq struct {
	sessionID string // 会话ID
	roomId    string // 房间ID
	clientId  string // 客户端ID
}
