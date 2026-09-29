package agentcli

import (
	"context"
	"errors"
	"sync"
)

// ErrTurnInFlight 同一会话已有在途轮次，新轮次被拒绝
var ErrTurnInFlight = errors.New("agentcli: turn already in flight for session")

// turn 一个会话的在途轮次
type turn struct {
	turnID    string             // 轮次 ID，用于结束时不误删新轮次的记录
	cancel    context.CancelFunc // 该轮 context 的取消函数
	cancelled bool               // 是否已被取消，保证同一轮只计数一次
}

// Registry 每会话在途轮次表
//
// 保证同一会话不并发两轮：会话内串行、跨会话并行
// 会话状态在 Agent 侧，本表只记录在途事实，所以会话落到哪条分片都不影响正确性
type Registry struct {
	mu    sync.Mutex       // 保护 turns
	turns map[string]*turn // sessionID -> 在途轮次
}

// NewRegistry 创建空的在途轮次表
//
// return: 在途轮次表实例
func NewRegistry() *Registry {
	return &Registry{turns: make(map[string]*turn)}
}

// Begin 登记一轮开始
//
// sessionID: 会话 ID
// turnID: 轮次 ID
// cancel: 该轮 context 的取消函数，取消时由 Cancel 调用
// return: 该会话已有在途轮次时返回 ErrTurnInFlight
func (r *Registry) Begin(sessionID, turnID string, cancel context.CancelFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.turns[sessionID]; exists {
		return ErrTurnInFlight
	}
	r.turns[sessionID] = &turn{turnID: turnID, cancel: cancel}
	return nil
}

// End 登记一轮结束
//
// 只有 turnID 与当前在途轮次一致时才清除，避免迟到的旧轮次误删新轮次的登记
// sessionID: 会话 ID
// turnID: 轮次 ID
func (r *Registry) End(sessionID, turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.turns[sessionID]; ok && t.turnID == turnID {
		delete(r.turns, sessionID)
	}
}

// Cancel 取消某会话的在途轮次
//
// 同一轮重复取消只有第一次返回 true，便于调用方按「轮次」而非「次数」计数
// sessionID: 会话 ID
// return: 确实取消了在途轮次返回 true，无在途轮次或已取消过返回 false
func (r *Registry) Cancel(sessionID string) bool {
	r.mu.Lock()
	t, ok := r.turns[sessionID]
	if !ok || t.cancelled {
		r.mu.Unlock()
		return false
	}
	t.cancelled = true
	r.mu.Unlock()
	t.cancel()
	return true
}

// TurnID 查询某会话在途轮次的 ID
//
// sessionID: 会话 ID
// return: 在途轮次的 ID 与是否存在
func (r *Registry) TurnID(sessionID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.turns[sessionID]
	if !ok {
		return "", false
	}
	return t.turnID, true
}

// Inflight 返回当前在途轮次数
//
// return: 在途轮次数量
func (r *Registry) Inflight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.turns)
}
