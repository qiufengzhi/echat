package asr_cli

import (
	"testing"

	asrpb "echat-backend/proto/asr"
)

// TestDisabledRecognizerIsSafe 识别器未启用时投递与结果读取都不 panic
//
// provider 配为空时 Init 不构造识别器，此前调用方会直接解引用空指针
func TestDisabledRecognizerIsSafe(t *testing.T) {
	previous := GlobalRecognizer
	GlobalRecognizer = nil
	t.Cleanup(func() { GlobalRecognizer = previous })

	if Enabled() {
		t.Fatal("未构造识别器时应为未启用")
	}
	if Push(&asrpb.AudioChunk{SessionId: "s-disabled"}) {
		t.Fatal("未启用时投递应返回 false")
	}

	results := Results()
	select {
	case _, ok := <-results:
		t.Fatalf("未启用时结果通道不应产出，实际 ok=%v", ok)
	default:
	}
}

// TestPushDropsWhenInputFull 输入队列满时投递返回 false 且不阻塞
func TestPushDropsWhenInputFull(t *testing.T) {
	previous := GlobalRecognizer
	GlobalRecognizer = &Recognizer{AudioIn: make(chan *asrpb.AudioChunk, 1)}
	t.Cleanup(func() { GlobalRecognizer = previous })

	if !Push(&asrpb.AudioChunk{SessionId: "s-1"}) {
		t.Fatal("队列未满时应投递成功")
	}
	if Push(&asrpb.AudioChunk{SessionId: "s-2"}) {
		t.Fatal("队列已满时应返回 false 而不是阻塞")
	}
}

// TestResultsFallsBackToRecognizer 识别器存在时结果通道取自识别器
func TestResultsFallsBackToRecognizer(t *testing.T) {
	previous := GlobalRecognizer
	want := make(chan *asrpb.TranscriptAudioChunk, 1)
	GlobalRecognizer = &Recognizer{AudioOut: want}
	t.Cleanup(func() { GlobalRecognizer = previous })

	if got := Results(); got != (<-chan *asrpb.TranscriptAudioChunk)(want) {
		t.Fatal("识别器存在时结果通道应来自识别器自身")
	}
}
