package global

import (
	"sync"
	"time"

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

// AIStateChangeCh 全局 AI 状态变更事件通道，迁移发生后写入，订阅方负责消费
var AIStateChangeCh = make(chan AIStateChange, 128)

// roomAI 记录单个房间的 AI 状态与最近活动时间
type roomAI struct {
	state      AIState   // 房间当前 AI 状态
	lastActive time.Time // 最近一次活动时间，用于在线静默超时判定
}

// aiStore 维护所有房间的 AI 状态，替代原先的全局开关
type aiStore struct {
	mu    sync.RWMutex       // 保护 rooms 的并发读写
	rooms map[string]*roomAI // roomID 到房间 AI 状态的映射
}

// AIStates 全局 AI 状态存储实例，供 room 与 sfu 两个包共同使用
var AIStates = &aiStore{
	rooms: make(map[string]*roomAI),
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

// Remove 删除房间的 AI 状态，房间销毁时调用，避免状态泄漏
func (s *aiStore) Remove(roomID string) {
	s.mu.Lock()
	delete(s.rooms, roomID)
	s.mu.Unlock()
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

// emit 记录状态迁移日志并把事件写入通道，通道满时丢弃事件，避免阻塞业务协程
// 静默超时、唤醒、休眠、按钮开关等所有迁移都经过这里，统一留痕
func (s *aiStore) emit(roomID string, state AIState) {
	logger.Infow("AI 进入"+state.Readable(), "roomID", roomID, "state", state.String())
	select {
	case AIStateChangeCh <- AIStateChange{RoomID: roomID, State: state}:
	default:
	}
}
