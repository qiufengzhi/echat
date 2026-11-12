package bwe

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// collectWriter 记录收到的包数量，用于断言路由是否正确
type collectWriter struct {
	// mu 保护 n，出队协程与测试协程并发访问
	mu sync.Mutex
	// n 已收到的包数
	n int
}

// Write 收到一个包并计数
//
// h: RTP 头，本实现不使用
// payload: RTP 载荷，本实现不使用
// attrs: 拦截器属性，本实现不使用
// return: 写入字节数与固定 nil 错误
func (w *collectWriter) Write(h *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
	w.mu.Lock()
	w.n++
	w.mu.Unlock()
	return len(payload), nil
}

// count 返回已收到的包数
//
// return: 包数
func (w *collectWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// newTestPacket 构造一个用于测试的最小 RTP 包
//
// ssrc: 流标识
// seq: 序列号
// return: 可写入整形器的头与载荷
func newTestPacket(ssrc uint32, seq uint16) (*rtp.Header, []byte) {
	h := &rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: seq, PayloadType: 111}
	return h, make([]byte, 80)
}

// TestPacerDropsOnQueueFull 验证队列达到包数上限后丢弃队首，且长度不会超过上限
func TestPacerDropsOnQueueFull(t *testing.T) {
	// Tick 设得极大，让出队协程不消费队列，从而能确定性地观察上限行为
	p := NewPacer(Config{MaxPackets: 2, MaxBytes: 1 << 20, Tick: time.Hour, InitialBitrate: 1})
	defer p.Close()
	p.AddStream(1, &collectWriter{})

	for i := 0; i < 5; i++ {
		h, payload := newTestPacket(1, uint16(i))
		p.Write(h, payload, nil)
	}
	if got := p.QueueDepth(); got != 2 {
		t.Fatalf("队列应被限制在 2 个包，实际 %d", got)
	}
}

// TestPacerRoutesBySSRC 验证出队时按 SSRC 把包投递到对应写入器
func TestPacerRoutesBySSRC(t *testing.T) {
	// 码率给得足够高，保证 100ms 内能放行若干包
	p := NewPacer(Config{MaxPackets: 100, MaxBytes: 1 << 20, Tick: 5 * time.Millisecond, InitialBitrate: 8_000_000})
	defer p.Close()

	w1 := &collectWriter{}
	w2 := &collectWriter{}
	p.AddStream(1, w1)
	p.AddStream(2, w2)

	for i := 0; i < 3; i++ {
		h1, pay1 := newTestPacket(1, uint16(i))
		p.Write(h1, pay1, nil)
		h2, pay2 := newTestPacket(2, uint16(i))
		p.Write(h2, pay2, nil)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w1.count() >= 3 && w2.count() >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("两个流都应收到 3 个包，实际 %d 与 %d", w1.count(), w2.count())
}

// TestPacerRemoveStream 验证移除流之后包会被丢弃而不是投递给旧写入器
func TestPacerRemoveStream(t *testing.T) {
	p := NewPacer(Config{MaxPackets: 100, MaxBytes: 1 << 20, Tick: 5 * time.Millisecond, InitialBitrate: 8_000_000})
	defer p.Close()

	w := &collectWriter{}
	p.AddStream(7, w)
	p.RemoveStream(7)

	h, payload := newTestPacket(7, 1)
	p.Write(h, payload, nil)
	time.Sleep(100 * time.Millisecond)

	if got := w.count(); got != 0 {
		t.Fatalf("流已移除，不应再收到包，实际收到 %d", got)
	}
}

// TestPacerCloseIdempotent 验证重复关闭不会 panic，且关闭后写入被安全丢弃
func TestPacerCloseIdempotent(t *testing.T) {
	p := NewPacer(Config{})
	if err := p.Close(); err != nil {
		t.Fatalf("首次关闭不应报错: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("重复关闭不应报错: %v", err)
	}

	h, payload := newTestPacket(1, 1)
	if n, err := p.Write(h, payload, nil); n != 0 || err != nil {
		t.Fatalf("关闭后写入应返回 0 与 nil，实际 %d 与 %v", n, err)
	}
}
