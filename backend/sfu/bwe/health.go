package bwe

import (
	"sync"
	"time"
)

// State 订阅者健康状态
type State string

const (
	// StateHealthy 带宽充足，整形器不丢包
	StateHealthy State = "healthy"
	// StateConstrained 带宽不足但队列可控，仅靠整形器限速丢弃
	StateConstrained State = "constrained"
	// StateDegraded 持续拥塞且队列打满，需要上层降级动作
	StateDegraded State = "degraded"
	// StateEvicted 不可恢复，应主动断连让客户端重连
	StateEvicted State = "evicted"
)

// Snapshot 一次健康采样的观测值
type Snapshot struct {
	// TargetBitrate 估计器当前目标码率，单位 bit/s
	TargetBitrate int
	// NeedBitrate 当前音频需求的 RTP 层码率，单位 bit/s，非正数时取默认需求
	NeedBitrate int
	// QueueDepth 整形器当前队列包数
	QueueDepth int
	// MaxQueue 整形器队列上限包数
	MaxQueue int
}

// HealthConfig 状态机阈值
//
// 双阈值加最小停留时间：快速反应、缓慢乐观，避免在临界点反复迁移
type HealthConfig struct {
	// NeedRatioEnter 进入 constrained 的 target/need 比例阈值
	NeedRatioEnter float64
	// NeedRatioExit 退出 constrained 的比例阈值，必须大于进入值
	NeedRatioExit float64
	// HoldEnter 进入更深状态的最短持续时长
	HoldEnter time.Duration
	// HoldRecover 恢复到更浅状态的最短持续时长
	HoldRecover time.Duration
	// HoldEvict 从 degraded 升级到 evicted 的最短持续时长
	HoldEvict time.Duration
	// EvictConfirmations 断连前需要的连续确认次数
	EvictConfirmations int
}

// DefaultHealthConfig 返回默认阈值
//
// return: 按语音场景调的默认配置，实际阈值应在拿到多档实测分布后再固化
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		NeedRatioEnter:     1.15,
		NeedRatioExit:      1.3,
		HoldEnter:          500 * time.Millisecond,
		HoldRecover:        3 * time.Second,
		HoldEvict:          10 * time.Second,
		EvictConfirmations: 2,
	}
}

// defaultNeedBitrate 一条 32kbps Opus 流在 RTP 层的近似需求
//
// 80 字节 payload 加 12 字节 RTP 头，20ms 一帧，折算约 37kbps；
// 这里取 37k 作为单流需求基线，多人房按订阅流数放大由调用方处理
const defaultNeedBitrate = 37000

// Tracker 单对端的健康状态机
//
// 由房间级协程统一驱动，不为每个对端单独起 ticker：
// 1000 个对端各自起协程会让定时器数量失控
type Tracker struct {
	// mu 保护全部状态字段
	mu sync.Mutex
	// state 当前状态
	state State
	// since 进入当前状态的时刻
	since time.Time
	// pending 待确认的目标状态，为空表示无待确认迁移
	pending State
	// pendingSince 待确认状态首次出现的时刻
	pendingSince time.Time
	// evictConfirm 连续确认次数
	evictConfirm int
}

// NewTracker 创建初始为 healthy 的状态机
//
// now: 当前时刻，用于计算停留时间
// return: 可直接驱动的新实例
func NewTracker(now time.Time) *Tracker {
	return &Tracker{state: StateHealthy, since: now}
}

// State 返回当前状态
//
// return: 当前健康状态
func (t *Tracker) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Tick 推进一步状态机，返回推进后的状态
//
// now: 当前时刻
// s: 本次采样值
// cfg: 阈值配置
// return: 推进后的状态；未达到停留时间时保持原状态
func (t *Tracker) Tick(now time.Time, s Snapshot, cfg HealthConfig) State {
	t.mu.Lock()
	defer t.mu.Unlock()

	want := t.evaluate(now, s, cfg)
	if want == t.state {
		t.pending = ""
		t.evictConfirm = 0
		return t.state
	}

	if t.pending != want {
		t.pending = want
		t.pendingSince = now
		t.evictConfirm = 0
		return t.state
	}

	hold := cfg.HoldEnter
	switch want {
	case StateHealthy:
		hold = cfg.HoldRecover
	case StateEvicted:
		hold = cfg.HoldEvict
	}
	if now.Sub(t.pendingSince) < hold {
		return t.state
	}

	// 断连代价最高，要求连续多次确认，避免一次抖动就把用户踢掉
	if want == StateEvicted {
		t.evictConfirm++
		if t.evictConfirm < cfg.EvictConfirmations {
			return t.state
		}
	}

	t.state = want
	t.since = now
	t.pending = ""
	t.evictConfirm = 0
	return t.state
}

// evaluate 依据采样值与当前状态算出目标状态，含滞回死区
//
// now: 当前时刻
// s: 本次采样值
// cfg: 阈值配置
// return: 目标状态
func (t *Tracker) evaluate(now time.Time, s Snapshot, cfg HealthConfig) State {
	need := s.NeedBitrate
	if need <= 0 {
		need = defaultNeedBitrate
	}
	ratio := float64(s.TargetBitrate) / float64(need)
	queueFull := s.MaxQueue > 0 && s.QueueDepth >= s.MaxQueue

	// 队列打满说明整形器已经在持续丢弃，优先判为 degraded
	if queueFull {
		if t.state == StateDegraded && now.Sub(t.since) >= cfg.HoldEvict {
			return StateEvicted
		}
		return StateDegraded
	}

	switch t.state {
	case StateDegraded:
		// 恢复门槛比进入高，避免贴边抖动
		if ratio >= cfg.NeedRatioExit {
			return StateHealthy
		}
		if ratio >= cfg.NeedRatioEnter {
			return StateConstrained
		}
		return StateDegraded
	case StateConstrained:
		if ratio >= cfg.NeedRatioExit {
			return StateHealthy
		}
		if ratio < cfg.NeedRatioEnter {
			return StateConstrained
		}
		// 落在双阈值之间的死区，保持现状
		return StateConstrained
	default:
		if ratio < cfg.NeedRatioEnter {
			return StateConstrained
		}
		return StateHealthy
	}
}
