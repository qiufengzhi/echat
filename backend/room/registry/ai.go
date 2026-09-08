package registry

import "sync"

// AIStateStore 房间 AI 状态存储：按短码读写 AI 顶层状态
// 取值 offline / standby / online，语义见 global.AIState；内存实现先承载，P7 换 Redis
type AIStateStore interface {
	// Get 返回房间当前 AI 状态串，未登记按 offline 处理
	Get(roomID string) string
	// Set 写入房间 AI 状态串
	Set(roomID string, state string)
	// Remove 删除房间 AI 状态（房间销毁时调用）
	Remove(roomID string)
}

// MemoryAIStore AIStateStore 的内存实现
type MemoryAIStore struct {
	// mu 保护 states 的并发读写
	mu sync.RWMutex
	// states roomID -> AI 状态串
	states map[string]string
}

// NewMemoryAIStore 构造空的内存 AI 状态存储
func NewMemoryAIStore() *MemoryAIStore {
	return &MemoryAIStore{states: make(map[string]string)}
}

// Get 实现 AIStateStore
func (m *MemoryAIStore) Get(roomID string) string {
	m.mu.RLock()
	s := m.states[roomID]
	m.mu.RUnlock()
	if s == "" {
		return "offline"
	}
	return s
}

// Set 实现 AIStateStore
func (m *MemoryAIStore) Set(roomID string, state string) {
	m.mu.Lock()
	m.states[roomID] = state
	m.mu.Unlock()
}

// Remove 实现 AIStateStore
func (m *MemoryAIStore) Remove(roomID string) {
	m.mu.Lock()
	delete(m.states, roomID)
	m.mu.Unlock()
}
