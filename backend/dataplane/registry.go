package dataplane

import (
	"sort"
	"sync"
	"sync/atomic"
)

var (
	// registryMu 保护 registry 的并发读写
	registryMu sync.RWMutex
	// registry 已登记的数据面结构，键是登记序号而非名字，避免同名结构互相覆盖
	registry = make(map[uint64]Stat)
	// nextStatID 登记序号发生器
	nextStatID atomic.Uint64
)

// registerStat 把一个结构登记进运行快照
//
// 用登记序号而非名字做键：会话级队列可能因会话重建而短暂同名，按名字索引会互相覆盖，
// 快照里看不到真实数量
// s: 待登记的结构
// return: 注销函数，调用后该结构不再出现在快照里
func registerStat(s Stat) func() {
	id := nextStatID.Add(1)

	registryMu.Lock()
	registry[id] = s
	registryMu.Unlock()

	return func() {
		registryMu.Lock()
		delete(registry, id)
		registryMu.Unlock()
	}
}

// SnapshotEntry 运行快照里的一条记录，字段与 Stat 一一对应
type SnapshotEntry struct {
	// Name 结构名，用于定位是哪一个队列
	Name string `json:"name"`
	// Kind 结构种类，取值 ring / counter
	Kind string `json:"kind"`
	// Backlog 当前积压元素数，计数器类固定为 0
	Backlog int `json:"backlog"`
	// Capacity 容量上限，计数器类固定为 0
	Capacity int `json:"capacity"`
	// Dropped 自启动以来的累计丢弃数
	Dropped int64 `json:"dropped"`
}

// Snapshot 返回当前全部数据面结构的运行快照
//
// 告警只在累计丢弃达到阈值后稀疏出现，队列积压却是连续量，快照用于在没有告警时查看现状
// 按种类与名字排序，保证同一状态下的输出稳定；无结构登记时返回空切片而非 nil
// return: 快照记录列表
func Snapshot() []SnapshotEntry {
	registryMu.RLock()
	stats := make([]Stat, 0, len(registry))
	for _, s := range registry {
		stats = append(stats, s)
	}
	registryMu.RUnlock()

	entries := make([]SnapshotEntry, 0, len(stats))
	for _, s := range stats {
		entries = append(entries, SnapshotEntry{
			Name:     s.StatName(),
			Kind:     s.StatKind(),
			Backlog:  s.StatBacklog(),
			Capacity: s.StatCapacity(),
			Dropped:  s.StatDropped(),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})
	return entries
}
