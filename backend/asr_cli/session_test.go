package asr_cli

import (
	"testing"

	"echat-backend/dataplane"
	asrpb "echat-backend/proto/asr"
)

// newTestRecognizer 构造一个不连接云端的识别器骨架
//
// 只用于验证分派与生命周期，不触发任何网络动作
// return: 可用的识别器实例
func newTestRecognizer() *Recognizer {
	return &Recognizer{
		AudioOut:  make(chan *asrpb.TranscriptAudioChunk, 1),
		sessions:  make(map[string]*asrSession),
		outputFan: make(chan sessionResult, 1),
	}
}

// TestSessionQueuesBeforeConnect 建连完成前音频在本会话队列排队，接收路径不做阻塞 IO
//
// 这是 T6 的核心：此前建连（含握手，上限 20 秒）就发生在接收循环里，
// 一次握手会堵住全站音频识别，并在队列满后把该会话首句的中段丢掉
func TestSessionQueuesBeforeConnect(t *testing.T) {
	r := newTestRecognizer()

	// 预先登记一个「建连中」的会话：trans 尚未写入
	s := &asrSession{id: "s-conn", queue: dataplane.NewRing[*asrpb.AudioChunk]("asr.test.conn", 4)}
	r.sessions[s.id] = s
	t.Cleanup(func() { s.queue.Close() })

	if got := r.getOrCreateSession(s.id, "room", "client"); got != s {
		t.Fatal("已登记的会话应直接复用，不重复建连")
	}

	for i := 0; i < 4; i++ {
		if outcome := s.queue.Push(&asrpb.AudioChunk{Seq: int64(i)}); outcome != dataplane.Accepted {
			t.Fatalf("第 %d 块入队应成功，得到 %s", i, outcome)
		}
	}
	if s.queue.Len() != 4 {
		t.Fatalf("建连期间音频应留在本会话队列里，实际 %d 块", s.queue.Len())
	}
}

// TestCloseSessionWithoutConnect 建连尚未完成时会话也能安全关闭
func TestCloseSessionWithoutConnect(t *testing.T) {
	r := newTestRecognizer()
	s := &asrSession{id: "s-nil-trans", queue: dataplane.NewRing[*asrpb.AudioChunk]("asr.test.nil", 2)}
	r.sessions[s.id] = s

	if s.transcription() != nil {
		t.Fatal("未建连时应返回 nil 而不是空指针以外的值")
	}

	r.closeSession(s.id, true)
	if _, ok := r.sessions[s.id]; ok {
		t.Fatal("关闭后会话应从容纳表中移除")
	}
	if !s.queue.Closed() {
		t.Fatal("关闭会话应同时关闭其待发送队列，让发送协程退出")
	}
	if outcome := s.queue.Push(&asrpb.AudioChunk{Seq: 1}); outcome != dataplane.Rejected {
		t.Fatalf("队列关闭后投递应被拒绝，得到 %s", outcome)
	}
}

// TestCloseSessionIsIdempotent 重复关闭不 panic，且不会重复收尾
func TestCloseSessionIsIdempotent(t *testing.T) {
	r := newTestRecognizer()
	s := &asrSession{id: "s-once", queue: dataplane.NewRing[*asrpb.AudioChunk]("asr.test.once", 2)}
	r.sessions[s.id] = s

	r.closeSession(s.id, true)
	// 第二次调用时会话已不在表中，应静默返回
	r.closeSession(s.id, true)

	if _, ok := r.sessions[s.id]; ok {
		t.Fatal("关闭后不应仍在会话表中")
	}
	if !s.queue.Closed() {
		t.Fatal("关闭会话应同时关闭其待发送队列")
	}
}
