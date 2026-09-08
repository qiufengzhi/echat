package signaling

import (
	"echat-backend/global"
	"echat-backend/room/gateway"

	"github.com/google/uuid"
)

// StartRevokedKick 启动吊销消费循环：用户会话被吊销（登出全部/改密/重置/封禁）时踢掉实时连接
// 该通道是 outbox 事件骨干落库前的进程内即时风扇，事件丢失可由下游投影兜底
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
	var targets []*gateway.Client
	for _, c := range gateway.AllClients() {
		if c.UserID != ev.UserID.String() {
			continue
		}
		if ev.KeepSessionID != uuid.Nil && c.SessionID == ev.KeepSessionID.String() {
			continue
		}
		targets = append(targets, c)
	}

	for _, c := range targets {
		logger.Warnw("会话吊销，踢下线", "userID", c.UserID, "sessionID", c.SessionID, "reason", ev.Reason)
		// 尽力推送 kicked 应用消息后再断连；前端以关闭码为准防丢
		sendToClient(c, MsgTypeKicked, map[string]string{"reason": ev.Reason}, c.RoomID)
		// WebTransport 无控制帧概念，仅对原生 WebSocket 发送关闭帧
		gateway.KickClose(c)
		disconnect(c, "", "kicked")
	}
}
