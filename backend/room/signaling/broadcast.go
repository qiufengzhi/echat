package signaling

import (
	"echat-backend/global"
)

// StartAIStateBroadcaster 启动协程，消费 global 的 AI 状态变更事件并广播给对应房间全体成员
// 唤醒词/休眠词/静默超时等发生在 sfu 与 global 包内的迁移，都靠它同步到前端
func StartAIStateBroadcaster() {
	go func() {
		for evt := range global.AIStateChangeCh {
			broadcastToRoom(evt.RoomID, "", MsgTypeAiStatus, AiToggleRes{
				State: evt.State.String(),
			})
		}
	}()
}
