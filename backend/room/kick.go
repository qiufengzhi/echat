package room

import (
	"time"

	"echat-backend/global"

	"github.com/gorilla/websocket"
	"github.com/google/uuid"
)

// kickedCloseCode 服务端主动踢下线使用的自定义关闭码（避开 WebSocket 保留码）
const kickedCloseCode = 4001

// StartRevokedKick 启动吊销消费循环：用户会话被吊销（登出全部/改密/重置/封禁）时踢掉实时连接
// 该通道是 outbox 事件骨干落库前的进程内即时风扇，spec §9.3 实时吊销联动
func StartRevokedKick() {
	go func() {
		for ev := range global.UserRevokedCh {
			kickConnections(ev)
		}
	}()
}

// kickConnections 按吊销事件找到目标连接：推送 kicked 消息后以 4001 关闭码断连
// ev 吊销事件；同一用户的全部连接都会被踢，KeepSessionID 指定的会话保留（改密时的当前连接）
func kickConnections(ev global.UserRevokedEvent) {
	clientLock.RLock()
	var targets []*Client
	for _, c := range allConnectedClients {
		if c.UserID != ev.UserID.String() {
			continue
		}
		if ev.KeepSessionID != uuid.Nil && c.SessionID == ev.KeepSessionID.String() {
			continue
		}
		targets = append(targets, c)
	}
	clientLock.RUnlock()

	for _, c := range targets {
		logger.Warnw("会话吊销，踢下线", "userID", c.UserID, "sessionID", c.SessionID, "reason", ev.Reason)
		// 先尽力推送 kicked 应用消息，再以自定义关闭码断连；前端以关闭码为准防丢
		sendToClient(c, MsgTypeKicked, map[string]string{"reason": ev.Reason}, c.RoomID)
		// WriteControl 允许与 writePump 并发，关闭帧可安全立即发送
		_ = c.Conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(kickedCloseCode, "session revoked"),
			time.Now().Add(time.Second))
		disconnect(c, "")
	}
}