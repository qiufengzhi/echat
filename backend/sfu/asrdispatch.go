// Package sfu 的识别结果分发：把「按房间路由」与「房间内的结果处理」拆开
//
// 分发器是 asr_cli.Results() 的唯一消费者，循环体内只做一次按房间的查表与入队；
// 唤醒/休眠网关、打断判定、LLM 投递这三件事移到各房间自己的订阅者里，
// 于是一个房间的处理变慢不再牵动其他房间，三件事也可以各自演进
package sfu

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"echat-backend/agentcli"
	"echat-backend/asr_cli"
	"echat-backend/dataplane"
	"echat-backend/global"
	"echat-backend/observability"
	asrpb "echat-backend/proto/asr"
	llmpb "echat-backend/proto/llm"
	"echat-backend/tts_cli"
	"echat-backend/wakeword"
)

// roomQueueCapacity 每个房间的待处理识别结果队列容量
//
// 识别结果是可丢数据：消费端落后时挤掉最老的，用户要听的是最新那句
const roomQueueCapacity = 32

// turnSeq 轮次序号发生器，保证同一进程内每轮 ID 唯一
var turnSeq atomic.Int64

// roomSubscriber 单个房间的识别结果订阅者
type roomSubscriber struct {
	// roomID 所属房间短码
	roomID string
	// queue 待处理的识别结果队列，由分发器投递、本房间消费协程取用
	queue *dataplane.Ring[*asrpb.TranscriptAudioChunk]
}

// asrDispatcher 按房间分发识别结果的订阅表
type asrDispatcher struct {
	// mu 保护 rooms 的并发读写
	mu sync.Mutex
	// rooms 房间短码到订阅者的映射，随房间创建建立、随房间销毁移除
	rooms map[string]*roomSubscriber
	// drop 无处可投的结果计数，例如房间已销毁后才到达的结果
	drop *dataplane.Counter
}

// GlobalASRDispatcher 全局分发器，StartASRDispatcher 之后可用
var GlobalASRDispatcher = &asrDispatcher{
	rooms: make(map[string]*roomSubscriber),
	drop:  dataplane.NewCounter("asr.dispatch", 0),
}

// StartASRDispatcher 启动分发协程：单一循环只做按房间路由，不做任何业务判断
//
// 必须在 asr_cli.Init() 之后调用；识别器未启用时结果通道永不产出，该协程静默等待
func StartASRDispatcher() {
	go func() {
		for res := range asr_cli.Results() {
			GlobalASRDispatcher.dispatch(res)
		}
	}()
}

// subscribe 为房间登记订阅者并启动其消费协程
//
// 同一房间重复订阅返回同一个订阅者：给出两个只会让两条消费循环互相抢事件
// roomID: 房间短码
// return: 该房间的订阅者
func (d *asrDispatcher) subscribe(roomID string) *roomSubscriber {
	d.mu.Lock()
	defer d.mu.Unlock()

	if sub, ok := d.rooms[roomID]; ok {
		return sub
	}
	sub := &roomSubscriber{
		roomID: roomID,
		queue:  dataplane.NewRing[*asrpb.TranscriptAudioChunk]("sfu.asr.room."+roomID, roomQueueCapacity),
	}
	d.rooms[roomID] = sub
	go sub.consume()
	return sub
}

// unsubscribe 移除房间订阅者并关闭其队列，房间销毁时调用
//
// roomID: 房间短码
func (d *asrDispatcher) unsubscribe(roomID string) {
	d.mu.Lock()
	sub, ok := d.rooms[roomID]
	if ok {
		delete(d.rooms, roomID)
	}
	d.mu.Unlock()

	if ok {
		sub.queue.Close()
	}
}

// dispatch 把一条识别结果路由到所属房间，永不阻塞
//
// 房间已销毁（或结果未带房间）时丢弃并计数：结果无处可去，留着只会占内存
// res: 云端识别结果，RoomId 用于路由
func (d *asrDispatcher) dispatch(res *asrpb.TranscriptAudioChunk) {
	d.mu.Lock()
	sub := d.rooms[res.RoomId]
	d.mu.Unlock()

	if sub == nil {
		d.drop.Drop("roomID", res.RoomId, "sessionID", res.SessionId)
		return
	}
	sub.queue.Push(res)
}

// consume 房间订阅者的消费循环
//
// 三段职责保持原有先后顺序：唤醒/休眠网关 → 打断判定 → 投递 LLM
func (s *roomSubscriber) consume() {
	for {
		res, ok := s.queue.Pop()
		if !ok {
			if s.queue.Closed() {
				return
			}
			<-s.queue.Wait()
			continue
		}
		handleASRResult(res)
	}
}

// handleASRResult 处理单条识别结果：按房间 AI 状态做网关，命中则投递一轮对话
//
// res: 云端识别结果
func handleASRResult(res *asrpb.TranscriptAudioChunk) {
	switch global.AIStates.Get(res.RoomId) {
	case global.AIOffline:
		// 离线：不监听、不唤醒，结果直接丢弃
		return

	case global.AIStandby:
		// 待机：只在句末匹配唤醒词，命中转在线；中间结果与未命中结果一律丢弃
		if !res.IsFinal {
			return
		}
		if wakeword.IsWake(res.Text) {
			global.AIStates.TryWake(res.RoomId)
			logger.Infow("命中唤醒词", "roomID", res.RoomId, "clientID", res.ClientId, "text", res.Text)
		}
		// 唤醒词本身不送 LLM，等下一句才是真正的输入
		return

	case global.AIOnline:
		// 在线：句末匹配休眠词，命中转待机且不送 LLM
		if res.IsFinal && wakeword.IsSleep(res.Text) {
			global.AIStates.TrySleep(res.RoomId)
			logger.Infow("命中休眠词", "roomID", res.RoomId, "clientID", res.ClientId, "text", res.Text)
			return
		}
		// 刷新活动时间，推迟静默超时
		global.AIStates.Touch(res.RoomId)
	}

	// 中间结果只用于唤醒与状态刷新，不进 LLM：半句喂给模型会污染上下文
	if !res.IsFinal {
		return
	}

	logger.Infow("收到识别结果", "userID", res.ClientId, "text", res.Text, "isFinal", res.IsFinal, "seq", res.Seq)
	// 本地判停 → ASR 句末延迟（用最近一帧有效语音时刻近似）
	observability.MarkASRFinal(res.SessionId)
	// 用户再次开口即打断：判定锚点是「AI 是否在出声」而不是「LLM RPC 是否在途」——
	// LLM 流已结束、TTS 仍在播尾巴的窗口里，若不打断，新回答会排在旧尾巴后面播出
	// 在途轮次：取消该轮 RPC 并结束当前轮的音频输出
	// 尾巴窗口（无在途轮次但音频在播）：只停播，供新轮次音频干净地顶上
	if runningTurn, inFlight := agentcli.Global.TurnID(res.SessionId); inFlight {
		logger.Infow("用户插话，打断在途轮次",
			"roomID", res.RoomId, "clientID", res.ClientId, "runningTurn", runningTurn)
		agentcli.Global.CancelTurn(res.SessionId)
		tts_cli.GlobalTTSService.Interrupt(res.SessionId)
		observability.MarkInterruptRequest(res.SessionId)
		observability.EndTurn(runningTurn, "interrupted")
	} else if playbackSpeaking(res.SessionId) {
		logger.Infow("用户插话，打断播放尾巴",
			"roomID", res.RoomId, "clientID", res.ClientId)
		tts_cli.GlobalTTSService.Interrupt(res.SessionId)
	}
	// 每个句末结果发起一轮对话：建 turn 根 span，随 SendTurn ctx 传播到 Python
	turnID := fmt.Sprintf("%s-%d", res.SessionId, turnSeq.Add(1))
	turnCtx := observability.StartTurn(context.Background(), turnID, res.SessionId, res.RoomId)
	if err := agentcli.Global.SendTurn(turnCtx, &llmpb.TurnRequest{
		SessionId: res.SessionId,
		RoomId:    res.RoomId,
		ClientId:  res.ClientId,
		UserText:  res.Text,
		TurnId:    turnID,
		Seq:       res.Seq,
	}); err != nil {
		logger.Warnw("发起对话轮次失败", "roomID", res.RoomId, "clientID", res.ClientId, "turnID", turnID, "error", err)
	}
}
