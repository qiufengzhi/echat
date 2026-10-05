package global

import (
	"sync"
	"time"

	"echat-backend/dataplane"
	"echat-backend/logging"
)

// logger 是 AI 状态机的日志实例
var logger = logging.New("ai")

// AIState 表示单个房间内 AI 助手所处的顶层状态
type AIState int

// AI 助手顶层状态取值
const (
	AIOffline AIState = iota // 离线：ASR 结果直接丢弃，既不监听唤醒词也不送 LLM
	AIStandby                // 待机：ASR 结果只过唤醒词网关，命中唤醒词才转在线
	AIOnline                 // 在线：ASR 结果送 LLM，并同时匹配休眠词
)

// String 返回状态的英文标识，用于信令下发与结构化字段，取值 offline/standby/online
func (s AIState) String() string {
	switch s {
	case AIOffline:
		return "offline"
	case AIStandby:
		return "standby"
	case AIOnline:
		return "online"
	default:
		return "offline"
	}
}

// Readable 返回状态的中文可读词，用于日志输出，取值 离线/待机/在线
func (s AIState) Readable() string {
	switch s {
	case AIOffline:
		return "离线"
	case AIStandby:
		return "待机"
	case AIOnline:
		return "在线"
	default:
		return "离线"
	}
}

// AIStateChange 表示一次 AI 状态迁移事件，供上层（room 包）订阅后广播给前端
type AIStateChange struct {
	RoomID string  // 发生迁移的房间 ID
	State  AIState // 迁移后的状态
}

// coalescedTotal 累计被合并掉的迁移次数
//
// 合并不是丢弃，但需要可见：合并率高说明订阅方处理不过来，值得被看见
var coalescedTotal = dataplane.NewCounter("ai.state.coalesced", 0)

// AIStateSub 单个房间的 AI 状态订阅
//
// 合并式：只保留最新状态，尚未取走的中间迁移被覆盖。状态是幂等的，订阅方只需要最终值，
// 因此「来不及取」的语义是合并而不是丢弃——丢弃会让前端停在错误状态且无人知道
type AIStateSub struct {
	mu        sync.Mutex // 保护 latest、coalesced 与 closed
	latest    *AIStateChange
	notify    chan struct{} // 容量 1 的通知信号，Close 时关闭
	coalesced int64         // 累计被覆盖掉的迁移次数
	closed    bool          // 订阅是否已关闭
}

// newAIStateSub 构造一个空订阅
//
// return: 订阅实例
func newAIStateSub() *AIStateSub {
	return &AIStateSub{notify: make(chan struct{}, 1)}
}

// Next 取出最新状态，永不阻塞
//
// 取走后槽位清空，直到下一次迁移才会再有新值
// return: 状态事件与是否取到，没有未取走的状态时返回零值与 false
func (s *AIStateSub) Next() (AIStateChange, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		return AIStateChange{}, false
	}
	evt := *s.latest
	s.latest = nil
	return evt, true
}

// Wait 返回等待通知的通道，有新状态或订阅被关闭时被唤醒
//
// return: 只读通知通道，订阅关闭后被关闭
func (s *AIStateSub) Wait() <-chan struct{} {
	return s.notify
}

// Closed 返回订阅是否已关闭
//
// return: 已关闭返回 true，此时 Next 取空后不再有新值
func (s *AIStateSub) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Coalesced 返回被覆盖掉的迁移次数
//
// return: 累计合并次数，0 表示每次迁移都被订阅方及时取走
func (s *AIStateSub) Coalesced() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coalesced
}

// publish 投递一次状态迁移，永不阻塞
//
// evt: 待投递的状态事件
// return: 覆盖掉了尚未取走的旧状态时返回 true
func (s *AIStateSub) publish(evt AIStateChange) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}

	overwrote := s.latest != nil
	if overwrote {
		s.coalesced++
	}
	s.latest = &evt

	select {
	case s.notify <- struct{}{}:
	default:
	}
	return overwrote
}

// Close 关闭订阅并唤醒等待者，幂等
//
// 关闭后不再接收新状态；房间销毁时调用，不调用会留下永不消费的订阅
func (s *AIStateSub) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.notify)
	s.mu.Unlock()
}

// roomAI 记录单个房间的 AI 状态与最近活动时间
type roomAI struct {
	state      AIState   // 房间当前 AI 状态
	lastActive time.Time // 最近一次活动时间，用于在线静默超时判定
}

// aiStore 维护所有房间的 AI 状态，替代原先的全局开关
type aiStore struct {
	mu    sync.RWMutex           // 保护 rooms 与 subs 的并发读写
	rooms map[string]*roomAI     // roomID 到房间 AI 状态的映射
	subs  map[string]*AIStateSub // roomID 到该房间状态订阅者的映射
}

// AIStates 全局 AI 状态存储实例，供 room 与 sfu 两个包共同使用
var AIStates = &aiStore{
	rooms: make(map[string]*roomAI),
	subs:  make(map[string]*AIStateSub),
}

// Subscribe 订阅指定房间的 AI 状态迁移
//
// 同一房间多次订阅返回同一个订阅者：状态是房间级的，多条消费循环只会互相抢事件
// roomID: 房间 ID
// return: 该房间的订阅，房间销毁时由 Remove 关闭
func (s *aiStore) Subscribe(roomID string) *AIStateSub {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub, ok := s.subs[roomID]; ok {
		return sub
	}
	sub := newAIStateSub()
	s.subs[roomID] = sub
	return sub
}

// Get 返回房间当前 AI 状态，房间不存在时按离线处理
func (s *aiStore) Get(roomID string) AIState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.rooms[roomID]; ok {
		return r.state
	}
	return AIOffline
}

// SetOnline 房间 AI 转在线，用于房主按钮开启，并广播迁移事件
func (s *aiStore) SetOnline(roomID string) {
	s.mu.Lock()
	s.rooms[roomID] = &roomAI{state: AIOnline, lastActive: time.Now()}
	s.mu.Unlock()
	s.emit(roomID, AIOnline)
}

// SetOffline 房间 AI 转离线，用于房主按钮关闭，并广播迁移事件
func (s *aiStore) SetOffline(roomID string) {
	s.mu.Lock()
	s.rooms[roomID] = &roomAI{state: AIOffline}
	s.mu.Unlock()
	s.emit(roomID, AIOffline)
}

// TryWake 在待机状态下命中唤醒词时转在线，返回是否真的发生了迁移
func (s *aiStore) TryWake(roomID string) bool {
	s.mu.Lock()
	r, ok := s.rooms[roomID]
	if !ok || r.state != AIStandby {
		s.mu.Unlock()
		return false
	}
	r.state = AIOnline
	r.lastActive = time.Now()
	s.mu.Unlock()
	s.emit(roomID, AIOnline)
	return true
}

// TrySleep 在在线状态下命中休眠词时转待机，返回是否真的发生了迁移
func (s *aiStore) TrySleep(roomID string) bool {
	s.mu.Lock()
	r, ok := s.rooms[roomID]
	if !ok || r.state != AIOnline {
		s.mu.Unlock()
		return false
	}
	r.state = AIStandby
	s.mu.Unlock()
	s.emit(roomID, AIStandby)
	return true
}

// Touch 刷新房间最近活动时间，仅在在线状态生效，用于延迟静默超时
func (s *aiStore) Touch(roomID string) {
	s.mu.Lock()
	if r, ok := s.rooms[roomID]; ok && r.state == AIOnline {
		r.lastActive = time.Now()
	}
	s.mu.Unlock()
}

// Remove 删除房间的 AI 状态与订阅，房间销毁时调用，避免状态与订阅泄漏
//
// roomID: 房间 ID
func (s *aiStore) Remove(roomID string) {
	s.mu.Lock()
	delete(s.rooms, roomID)
	sub, hasSub := s.subs[roomID]
	if hasSub {
		delete(s.subs, roomID)
	}
	s.mu.Unlock()

	if hasSub {
		sub.Close()
	}
}

// StartStandbyCleanup 启动后台协程，定时把静默超时的在线房间转待机
// interval 是扫描间隔，timeout 是静默超时时长，由调用方从配置读取后传入
func (s *aiStore) StartStandbyCleanup(interval time.Duration, timeout time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			var changed []string
			s.mu.Lock()
			for id, r := range s.rooms {
				if r.state == AIOnline && now.Sub(r.lastActive) > timeout {
					r.state = AIStandby
					changed = append(changed, id)
				}
			}
			s.mu.Unlock()
			for _, id := range changed {
				s.emit(id, AIStandby)
			}
		}
	}()
}

// emit 记录状态迁移日志，并把事件投递给该房间的订阅者
//
// 静默超时、唤醒、休眠、按钮开关等所有迁移都经过这里，统一留痕
// 房间无订阅者时不投递也不排队：状态是幂等的，订阅方新建后读到的就是当前值
// roomID: 发生迁移的房间；state: 迁移后的状态
func (s *aiStore) emit(roomID string, state AIState) {
	logger.Infow("AI 进入"+state.Readable(), "roomID", roomID, "state", state.String())

	s.mu.RLock()
	sub := s.subs[roomID]
	s.mu.RUnlock()
	if sub == nil {
		return
	}

	if sub.publish(AIStateChange{RoomID: roomID, State: state}) {
		if total, report := coalescedTotal.Inc(); report {
			logger.Infow("AI 状态迁移被合并", "roomID", roomID, "coalescedTotal", total)
		}
	}
}
