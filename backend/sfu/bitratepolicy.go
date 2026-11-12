package sfu

import (
	"time"

	"echat-backend/observability"
	"echat-backend/sfu/bwe"
)

// bitrateLadder 房间级 Opus 码率档位，从高到低
//
// 对应 CBR 下 20ms 帧的 payload 约为 80、60、40 字节
var bitrateLadder = []int{32000, 24000, 16000}

// degradeRatio 触发降档的「degraded 及以上」人数占比门槛
//
// 门槛的意义是阻止单个极端对端绑架全房间：单对端的问题应由整形器丢包或断连解决
const degradeRatio = 0.3

// upgradeHold 升档需要持续满足条件的时长，即升慢降快
const upgradeHold = 30 * time.Second

// bitrateCheckEvery 每多少帧检查一次房间码率档位，按 20ms 一帧折算为 5 秒
const bitrateCheckEvery = 250

// roomBitratePolicy 房间级码率策略在一次 AI 会话内的状态
type roomBitratePolicy struct {
	// index 当前档位在 bitrateLadder 中的下标
	index int
	// upgradeSince 连续满足升档条件的起始时刻，零值表示当前不满足
	upgradeSince time.Time
}

// applyRoomBitrate 依据房间内健康状态分布调整编码码率档位
//
// 编码器是房间内所有订阅者共享的（编码一次发给所有人），
// 所以降码率只能按房间做；单对端的正确处置是整形器丢包或断连
//
// enc: 本房间共享的编码器
// p: 会话内保存的档位状态
// return: 本次是否发生了档位变化
func (r *SFURoom) applyRoomBitrate(enc OpusEncoder, p *roomBitratePolicy) bool {
	r.lock.RLock()
	total := 0
	bad := 0
	for _, peer := range r.peers {
		if peer.health == nil {
			continue
		}
		total++
		switch peer.health.State() {
		case bwe.StateDegraded, bwe.StateEvicted:
			bad++
		}
	}
	r.lock.RUnlock()

	if total == 0 {
		return false
	}
	now := time.Now()

	// 降档：占比达到门槛立即生效，不等待
	if float64(bad)/float64(total) >= degradeRatio {
		p.upgradeSince = time.Time{}
		if p.index >= len(bitrateLadder)-1 {
			return false
		}
		p.index++
		return r.switchRoomBitrate(enc, p.index, "down")
	}

	// 只要还有任何一个对端处于 degraded，就不考虑升档
	if bad > 0 {
		p.upgradeSince = time.Time{}
		return false
	}
	if p.index == 0 {
		return false
	}
	if p.upgradeSince.IsZero() {
		p.upgradeSince = now
		return false
	}
	if now.Sub(p.upgradeSince) < upgradeHold {
		return false
	}
	p.index--
	p.upgradeSince = time.Time{}
	return r.switchRoomBitrate(enc, p.index, "up")
}

// switchRoomBitrate 应用新档位并记录指标
//
// enc: 本房间共享的编码器
// index: 目标档位下标
// action: 动作名称，取 up 或 down
// return: 固定返回 true，表示发生了切换
func (r *SFURoom) switchRoomBitrate(enc OpusEncoder, index int, action string) bool {
	bps := bitrateLadder[index]
	if err := enc.SetBitrate(bps); err != nil {
		logger.Warnw("调整房间编码码率失败", "roomID", shortID(r.ID), "bitrate", bps, "error", err)
		return false
	}
	observability.RecordOpusBitrate(bps)
	observability.RecordBitrateAction(action)
	logger.Infow("房间编码码率已调整",
		"roomID", shortID(r.ID), "bitrate", bps, "action", action)
	return true
}
