package tts_cli

import (
	"sync"

	"echat-backend/dataplane"
)

// AudioEventKind 音频输出端点上的事件类型
type AudioEventKind uint8

// 事件类型取值
const (
	// AudioData 合成音频片段
	AudioData AudioEventKind = iota
	// AudioInterrupt 当前轮已被打断，接收方应丢弃已缓冲尚未播出的音频
	AudioInterrupt
)

// String 返回事件类型名，用于日志字段，取值 audio_data / audio_interrupt
func (k AudioEventKind) String() string {
	if k == AudioInterrupt {
		return "audio_interrupt"
	}
	return "audio_data"
}

// AudioEvent 音频输出端点上的一个事件
type AudioEvent struct {
	// Kind 事件类型
	Kind AudioEventKind
	// TurnID 该片段发布时的有效轮次，Kind 为 AudioInterrupt 时为空
	TurnID string
	// SentenceSeq 该片段所属的轮内句子序号（从 1 计），Kind 为 AudioInterrupt 时为 0；
	// 播放侧据此回答「用户实际听到了第几句」，供被打断半句的历史裁剪
	SentenceSeq int32
	// PCM 合成音频的 PCM16 小端字节，Kind 为 AudioInterrupt 时为空
	PCM []byte
}

// deadTurnLimit 每会话保留的已作废轮次标识上限
//
// 只用于识别迟到 token，正常流程下一轮开始时上一轮就已作废，不需要长期保留
const deadTurnLimit = 16

// staleSegmentDropped 累计丢弃的过期音频片段
//
// 进程级共享：这里要回答的是「有没有发生过、频率多高」，
// 按会话拆分会让运行快照里堆满只存活几分钟的计数器
var staleSegmentDropped = dataplane.NewCounter("tts.stale_segment", 0)

// endpoint 会话级音频输出端点
//
// 与会话同寿命，跨合成执行器换代保持可用：执行器可以随轮次销毁重建，
// 播放侧只认端点，因此不会出现「执行器重建之后没人消费」的失联
type endpoint struct {
	// events 端点事件队列，丢最老：播放侧永远想要最新的音频
	events *dataplane.Ring[AudioEvent]
	// mu 保护下列全部字段，同时把「判断有效性」与「推入事件」串成原子
	mu sync.Mutex
	// turn 当前有效轮次标识
	turn string
	// active 当前轮是否有效，被打断后为假，其后的片段一律丢弃
	active bool
	// dead 已结束或被取代的轮次标识，用于识别迟到 token
	dead map[string]struct{}
	// deadOrder 按作废先后记录 dead 的键，用于按上限淘汰
	deadOrder []string
	// pumped 当前挂泵的合成执行器
	//
	// 锁内比对执行器身份而非轮次：执行器在轮次之间复用，同一执行器的输出来自哪一轮
	// 无从分辨，所以只能按「这条执行器还是不是当前那条」判定是否过期
	pumped *SessionPipeline
}

// newEndpoint 构造会话级音频输出端点
//
// sessionID: 会话标识，用于端点命名与运行快照定位
// capacity: 事件队列容量，小于 1 时按 1 处理
// return: 端点实例
func newEndpoint(sessionID string, capacity int) *endpoint {
	return &endpoint{
		events: dataplane.NewRing[AudioEvent]("tts.endpoint."+sessionID, capacity),
		dead:   make(map[string]struct{}),
	}
}

// Next 取出端点上的下一个事件，永不阻塞
//
// 播放侧的消费循环是「先 Next，取空后等 Wait，唤醒后再判断 Closed」三步
// 同一端点不允许两条循环同时消费——事件只会被其中一个取走
// return: 事件与是否取到，无事件时返回零值与 false
func (e *endpoint) Next() (AudioEvent, bool) {
	return e.events.Pop()
}

// Wait 返回等待通知的通道，端点有新事件或被关闭时被唤醒
//
// return: 只读通知通道，端点关闭后被关闭
func (e *endpoint) Wait() <-chan struct{} {
	return e.events.Wait()
}

// Closed 返回端点是否已关闭
//
// return: 已关闭返回 true，此时 Next 排空后不再有新事件
func (e *endpoint) Closed() bool {
	return e.events.Closed()
}

// beginTurn 把端点切换到新轮次，并把上一轮标为作废
//
// turnID: 新轮次标识，由调用方保证非空
func (e *endpoint) beginTurn(turnID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.turn != "" && e.turn != turnID {
		e.markDeadLocked(e.turn)
	}
	e.turn = turnID
	e.active = true
}

// isDead 判断某轮次是否已结束或被取代
//
// turnID: 轮次标识
// return: 已作废返回 true，其 token 不应再进入断句缓冲
func (e *endpoint) isDead(turnID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.dead[turnID]
	return ok
}

// markDead 把某轮次标为已结束
//
// 轮次正常收尾（收到 IsFinal）时调用，此后该轮的迟到 token 一律丢弃
// turnID: 轮次标识
func (e *endpoint) markDead(turnID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.markDeadLocked(turnID)
}

// markDeadLocked 在持锁前提下登记作废轮次，超过上限时淘汰最早的一条
//
// turnID: 轮次标识，为空时忽略
func (e *endpoint) markDeadLocked(turnID string) {
	if turnID == "" {
		return
	}
	if _, ok := e.dead[turnID]; ok {
		return
	}
	if len(e.deadOrder) >= deadTurnLimit {
		delete(e.dead, e.deadOrder[0])
		e.deadOrder = e.deadOrder[1:]
	}
	e.dead[turnID] = struct{}{}
	e.deadOrder = append(e.deadOrder, turnID)
}

// attach 把合成执行器登记为端点当前的泵源
//
// sp: 本轮使用的合成执行器
// return: 该执行器尚未挂泵时返回 true，调用方据此决定是否启动泵协程
func (e *endpoint) attach(sp *SessionPipeline) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.pumped == sp {
		return false
	}
	e.pumped = sp
	return true
}

// publishFrom 把一段合成音频发布到端点，永不阻塞
//
// 满足任一条件即丢弃并计数：轮次已被打断、或数据来自已被换代的执行器。
// 取消 RPC 与销毁执行器都是尽力而为，在途音频一定会有一小段晚于打断到达
// sp: 产出该音频的合成执行器；chunk: PCM16 小端字节，带句子序号
func (e *endpoint) publishFrom(sp *SessionPipeline, chunk synthChunk) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.active || e.pumped != sp {
		staleSegmentDropped.Drop("endpoint", e.events.Name())
		return
	}
	e.events.Push(AudioEvent{Kind: AudioData, TurnID: e.turn, SentenceSeq: chunk.sentenceSeq, PCM: chunk.pcm})
}

// interrupt 打断当前轮：标废当前轮、标记失效、投递打断事件
//
// 三件事在同一把锁内完成，因此打断事件一定排在所有在途音频之后，
// 不会出现「接收方 drain 完又被塞进一段旧音频」
func (e *endpoint) interrupt() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.markDeadLocked(e.turn)
	e.active = false
	e.events.Push(AudioEvent{Kind: AudioInterrupt})
}

// close 关闭端点，唤醒所有订阅者
func (e *endpoint) close() {
	e.events.Close()
}
