package registry

import (
	"sync"

	"echat-backend/room/aggregate"
)

// RoomRegistry 在线房间实例注册表：存/取/删信令房间
// 内存实现承载 active 房间实例；P7 可换 Redis 实现做跨进程共享（仅镜像房间状态，不含真实连接）
type RoomRegistry interface {
	// Get 按短码取房间实例，不存在返回 false
	Get(roomID string) (*aggregate.Room, bool)
	// Put 写入或覆盖房间实例
	Put(roomID string, room *aggregate.Room)
	// Delete 删除房间实例
	Delete(roomID string)
	// Exists 判断房间是否已注册
	Exists(roomID string) bool
	// All 返回全部房间实例的副本（清理协程遍历用，改动不影响内部表）
	All() map[string]*aggregate.Room
}

// MemoryRegistry RoomRegistry 的内存实现，行为 = 原 room 包 allSignalRooms 的 map + RWMutex
type MemoryRegistry struct {
	// mu 保护 rooms 的并发读写
	mu sync.RWMutex
	// rooms 短码 -> 房间实例
	rooms map[string]*aggregate.Room
}

// NewMemoryRegistry 构造空的内存房间注册表
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{rooms: make(map[string]*aggregate.Room)}
}

// Get 实现 RoomRegistry
func (m *MemoryRegistry) Get(roomID string) (*aggregate.Room, bool) {
	m.mu.RLock()
	r, ok := m.rooms[roomID]
	m.mu.RUnlock()
	return r, ok
}

// Put 实现 RoomRegistry
func (m *MemoryRegistry) Put(roomID string, room *aggregate.Room) {
	m.mu.Lock()
	m.rooms[roomID] = room
	m.mu.Unlock()
}

// Delete 实现 RoomRegistry
func (m *MemoryRegistry) Delete(roomID string) {
	m.mu.Lock()
	delete(m.rooms, roomID)
	m.mu.Unlock()
}

// Exists 实现 RoomRegistry
func (m *MemoryRegistry) Exists(roomID string) bool {
	m.mu.RLock()
	_, ok := m.rooms[roomID]
	m.mu.RUnlock()
	return ok
}

// All 实现 RoomRegistry
func (m *MemoryRegistry) All() map[string]*aggregate.Room {
	m.mu.RLock()
	out := make(map[string]*aggregate.Room, len(m.rooms))
	for id, r := range m.rooms {
		out[id] = r
	}
	m.mu.RUnlock()
	return out
}
