package sfu

import (
	"time"

	"echat-backend/config"
	"echat-backend/observability"
	"echat-backend/sfu/bwe"
)

// shortID 截取标识前 8 位用于日志，不足 8 位时原样返回
//
// id: 客户端或房间标识
// return: 供日志展示的短标识
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// healthTickInterval 健康采样间隔
//
// 200ms 足以覆盖状态机 500ms 级的最短停留，又不至于让房间协程空转
const healthTickInterval = 200 * time.Millisecond

// healthLoop 房间级健康监控协程，统一驱动房间内所有对端的状态机
//
// 不为每个对端单独起 ticker：1000 个对端会产生 1000 个定时器，
// 而房间数远小于对端数，按房间轮询的代价可以忽略
func (r *SFURoom) healthLoop() {
	ticker := time.NewTicker(healthTickInterval)
	defer ticker.Stop()

	cfg := config.Get().SFU.BWE
	hc := bwe.DefaultHealthConfig()
	for {
		select {
		case <-r.stopHealth:
			return
		case now := <-ticker.C:
			if !cfg.Enabled {
				continue
			}
			r.tickHealth(now, cfg, hc)
		}
	}
}

// tickHealth 对一个房间内的所有对端推进一步状态机
//
// now: 当前时刻，用于计算停留时间
// cfg: 拥塞控制配置，提供整形器队列上限
// hc: 状态机阈值
func (r *SFURoom) tickHealth(now time.Time, cfg config.BWEConfig, hc bwe.HealthConfig) {
	r.lock.RLock()
	peers := make([]*SFUPeer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.lock.RUnlock()

	for _, p := range peers {
		if p.bwe == nil || p.health == nil {
			continue
		}
		snap := bwe.Snapshot{
			TargetBitrate: p.bwe.TargetBitrate(),
			QueueDepth:    p.bwe.QueueDepth(),
			MaxQueue:      cfg.PacerMaxPackets,
		}
		prev := p.health.State()
		next := p.health.Tick(now, snap, hc)
		if next == prev {
			continue
		}

		observability.RecordHealthTransition(string(prev), string(next))
		logger.Infow("订阅者健康状态迁移",
			"clientID", shortID(p.ClientID),
			"from", string(prev),
			"to", string(next),
			"targetBitrate", snap.TargetBitrate,
			"queueDepth", snap.QueueDepth)

		if next == bwe.StateEvicted {
			// 主动断连：把「不可恢复的坏」转成「可恢复的坏」，客户端据此重连
			observability.RecordPeerDisconnect("evicted")
			pc := p.PC
			go func() {
				if pc != nil {
					_ = pc.Close()
				}
			}()
		}
	}
}
