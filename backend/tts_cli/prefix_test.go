package tts_cli

import (
	"testing"
)

// TestSentenceBufferDispatchAndPlayedPrefix 掌握播出前缀的取材路径：
// 送句时序号单调递增且与句子文本对齐，PlayedPrefix 按完整听到的句数截取
func TestSentenceBufferDispatchAndPlayedPrefix(t *testing.T) {
	svc := newTestService(t)
	const session = "s-prefix"

	sb := &sentenceBuffer{
		sp: &SessionPipeline{sentenceCh: make(chan taggedSentence, 8)},
	}
	svc.buffer[session] = sb

	sentences := []string{"第一句。", "第二句。", "第三句。"}
	for _, s := range sentences {
		if !sb.dispatch(s) {
			t.Fatalf("句子 %q 送入合成失败", s)
		}
	}
	if sb.sentenceSeq != int32(len(sentences)) {
		t.Fatalf("序号应为 %d，实际 %d", len(sentences), sb.sentenceSeq)
	}

	if got := svc.PlayedPrefix(session, 2); got != "第一句。第二句。" {
		t.Fatalf("完整听到前两句，前缀应为「第一句。第二句。」，实际 %q", got)
	}
	if got := svc.PlayedPrefix(session, 0); got != "" {
		t.Fatalf("一句都没完整听到，前缀应为空串，实际 %q", got)
	}
	if got := svc.PlayedPrefix(session, 99); got != "第一句。第二句。第三句。" {
		t.Fatalf("n 超过已送句数时应按实际句数截断，实际 %q", got)
	}
	if got := svc.PlayedPrefix("no-such-session", 1); got != "" {
		t.Fatalf("无缓冲的会话前缀应为空串，实际 %q", got)
	}
}

// TestSentenceBufferDispatchRejectWhenQueueFull 合成队列满时句子被丢弃且不占序号，
// 保证序号与实际合成播出的句子一一对应
func TestSentenceBufferDispatchRejectWhenQueueFull(t *testing.T) {
	sb := &sentenceBuffer{
		sp: &SessionPipeline{sentenceCh: make(chan taggedSentence, 1)},
	}

	if !sb.dispatch("第一句。") {
		t.Fatal("队列未满，首句应送入成功")
	}
	if sb.dispatch("第二句。") {
		t.Fatal("队列已满，第二句应被拒绝")
	}
	if sb.sentenceSeq != 1 {
		t.Fatalf("被拒绝的句子不应消耗序号，实际 %d", sb.sentenceSeq)
	}
	if len(sb.sentences) != 1 {
		t.Fatalf("被拒绝的句子不应记入 sentences，实际 %d 条", len(sb.sentences))
	}
}

// TestAudioEventCarriesSentenceSeq 句子序号随音频事件透传到端点，
// 播放侧据此回答「用户听到了第几句」
func TestAudioEventCarriesSentenceSeq(t *testing.T) {
	ep := newEndpoint("s-seq", 8)
	defer ep.close()

	sp := &SessionPipeline{}
	ep.beginTurn("t1")
	ep.attach(sp)

	ep.publishFrom(sp, synthChunk{sentenceSeq: 2, pcm: []byte{1}})
	ev, ok := ep.Next()
	if !ok {
		t.Fatal("事件缺失")
	}
	if ev.Kind != AudioData || ev.TurnID != "t1" || ev.SentenceSeq != 2 {
		t.Fatalf("音频事件应携带轮次与句子序号，得到 %+v", ev)
	}
}
