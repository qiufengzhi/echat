package signaling

import (
	"echat-backend/global"
)

// startAIStateBroadcast 为指定房间订阅 AI 状态迁移，并把迁移广播给该房间全体成员
//
// 每个房间一条消费协程、一个合并式订阅：
//   - 订阅在房间创建时建立，随房间销毁关闭，迁移只影响本房间
//   - 消费不及时的中间迁移被合并掉，前端只会看到最终状态，不会停在错误的中间值
//   - 唤醒词/休眠词/静默超时等发生在 sfu 与 global 包内的迁移，都靠它同步到前端
//
// roomID: 房间短码
// sub: 该房间的状态订阅，由调用方从 global.AIStates 取得
func startAIStateBroadcast(roomID string, sub *global.AIStateSub) {
	go func() {
		for {
			if evt, ok := sub.Next(); ok {
				broadcastToRoom(roomID, "", MsgTypeAiStatus, AiToggleRes{
					State: evt.State.String(),
				})
				continue
			}
			if sub.Closed() {
				return
			}
			<-sub.Wait()
		}
	}()
}
