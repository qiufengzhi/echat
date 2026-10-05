package sfu

import "testing"

// TestDrainFrames 清空帧队列并返回丢弃帧数
//
// 这是打断链路里「立即静音」的那一步：不清空的话，已经编码好的帧会继续按 20ms
// 的节奏播完，用户听到的是上一轮的尾巴
func TestDrainFrames(t *testing.T) {
	queue := make(chan queuedFrame, 4)
	for i := 0; i < 3; i++ {
		queue <- queuedFrame{payload: []byte{byte(i)}}
	}

	if got := drainFrames(queue); got != 3 {
		t.Fatalf("期望丢弃 3 帧，实际 %d", got)
	}
	if len(queue) != 0 {
		t.Fatalf("清空后队列应为空，实际剩 %d 帧", len(queue))
	}
}

// TestDrainFramesEmpty 空队列返回 0 且不阻塞
func TestDrainFramesEmpty(t *testing.T) {
	queue := make(chan queuedFrame, 4)
	if got := drainFrames(queue); got != 0 {
		t.Fatalf("空队列期望丢弃 0 帧，实际 %d", got)
	}
}

// TestDrainFramesPartial 队列未满时同样清空，不影响后续入队
func TestDrainFramesPartial(t *testing.T) {
	queue := make(chan queuedFrame, 8)
	queue <- queuedFrame{payload: []byte{1}}

	if got := drainFrames(queue); got != 1 {
		t.Fatalf("期望丢弃 1 帧，实际 %d", got)
	}

	queue <- queuedFrame{payload: []byte{2}}
	if got := drainFrames(queue); got != 1 {
		t.Fatalf("清空后仍可继续入队并再次清空，实际丢弃 %d 帧", got)
	}
}
