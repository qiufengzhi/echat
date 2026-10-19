package sfu

import (
	"testing"
	"time"
)

// TestPlaybackSpeaking 掌握播放态的判定：发帧即在播，静默超过宽限期即停播
func TestPlaybackSpeaking(t *testing.T) {
	ps := &playbackState{}
	if ps.speaking() {
		t.Fatalf("从未发过帧，不应视为在播")
	}

	ps.markSent("turn-1", 3)
	if !ps.speaking() {
		t.Fatalf("刚发过帧，应视为在播")
	}
	if got := ps.played("turn-1"); got != 3 {
		t.Fatalf("turn-1 已播到第 3 句，实际 %d", got)
	}
	if got := ps.played("turn-2"); got != 0 {
		t.Fatalf("turn-2 从未播出，应返回 0，实际 %d", got)
	}

	// 时间回拨超过宽限期：视为停播
	ps.mu.Lock()
	ps.lastSentAt = time.Now().Add(-speakingGrace - 50*time.Millisecond)
	ps.mu.Unlock()
	if ps.speaking() {
		t.Fatalf("静默超过宽限期，不应视为在播")
	}
}

// TestPlaybackRegistry 掌握登记表的行为：同会话幂等登记、未登记查询为缺省
func TestPlaybackRegistry(t *testing.T) {
	const sessionID = "sess-playback-reg"

	unregisterPlayback(sessionID) // 保证起点干净

	if playbackSpeaking(sessionID) {
		t.Fatalf("未登记的会话不应视为在播")
	}

	a := registerPlayback(sessionID)
	b := registerPlayback(sessionID)
	if a != b {
		t.Fatalf("同一会话重复登记应返回同一实例")
	}

	a.markSent("turn-9", 1)
	if !playbackSpeaking(sessionID) {
		t.Fatalf("登记并发帧后应视为在播")
	}
	if got := playbackPlayed(sessionID, "turn-9"); got != 1 {
		t.Fatalf("已播句子序号应为 1，实际 %d", got)
	}

	unregisterPlayback(sessionID)
	if playbackSpeaking(sessionID) {
		t.Fatalf("注销后不应再视为在播")
	}
	if got := playbackPlayed(sessionID, "turn-9"); got != 0 {
		t.Fatalf("注销后已播句子序号应返回 0，实际 %d", got)
	}
}
