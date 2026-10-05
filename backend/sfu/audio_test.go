package sfu

import (
	"slices"
	"testing"
)

// testOpusPacket 用项目自己的编码器编出一帧可解码的 Opus 数据
//
// 用真实码流而不是构造字节：解码路径的正确性只有在真实 payload 上才有意义
// t: 测试上下文
// return: 编码后的 Opus 帧
func testOpusPacket(t *testing.T) []byte {
	t.Helper()

	enc, err := newOpusEncoderPreset()
	if err != nil {
		t.Fatalf("创建 Opus 编码器失败: %v", err)
	}
	defer enc.Close()

	// 一段确定性的非静音采样，避免编码器走静音优化路径
	frame := make([]int16, opusEncoderFrameSamples)
	for i := range frame {
		frame[i] = int16((i*37)%2000 - 1000)
	}

	packet, err := enc.Encode(frame)
	if err != nil {
		t.Fatalf("Opus 编码失败: %v", err)
	}
	if len(packet) == 0 {
		t.Fatal("编码结果为空")
	}
	return packet
}

// TestDecodeDTXPayloadReturnsZero 空 payload 视为 DTX 静音包，返回 0 采样且不报错
func TestDecodeDTXPayloadReturnsZero(t *testing.T) {
	dec, err := newOpusDecoder(asrSampleRate)
	if err != nil {
		t.Fatalf("创建解码器失败: %v", err)
	}
	out := make([]int16, opusMaxFrameSamples)

	for _, payload := range [][]byte{nil, {}} {
		n, decErr := decodeOpusInto(dec, payload, out)
		if decErr != nil {
			t.Fatalf("DTX 包不应报错: %v", decErr)
		}
		if n != 0 {
			t.Fatalf("DTX 包应返回 0 采样，实际 %d", n)
		}
	}
}

// TestReusedDecoderMatchesFresh 复用解码器与新建解码器在同一起点上解同一帧，结果一致
//
// 解码器带帧间预测状态，因此这只对序列首帧成立；首帧一致即可证明复用没有引入
// 缓冲偏移或状态串扰，这正是把「每包新建解码器」改成「每发布者一个」时最需要保证的性质
func TestReusedDecoderMatchesFresh(t *testing.T) {
	packet := testOpusPacket(t)

	reused, err := newOpusDecoder(asrSampleRate)
	if err != nil {
		t.Fatalf("创建复用解码器失败: %v", err)
	}
	shared := make([]int16, opusMaxFrameSamples)
	nReused, err := decodeOpusInto(reused, packet, shared)
	if err != nil {
		t.Fatalf("复用解码器解码失败: %v", err)
	}

	fresh, err := newOpusDecoder(asrSampleRate)
	if err != nil {
		t.Fatalf("创建新建解码器失败: %v", err)
	}
	freshOut := make([]int16, opusMaxFrameSamples)
	nFresh, err := decodeOpusInto(fresh, packet, freshOut)
	if err != nil {
		t.Fatalf("新建解码器解码失败: %v", err)
	}

	if nReused != nFresh {
		t.Fatalf("采样数不一致：复用 %d，新建 %d", nReused, nFresh)
	}
	if nReused != 320 {
		t.Fatalf("20ms 帧在 16kHz 下应解出 320 个采样，实际 %d", nReused)
	}
	if !slices.Equal(shared[:nReused], freshOut[:nFresh]) {
		t.Fatal("复用解码器与新建解码器的解码结果不一致")
	}
}

// TestReusedDecoderHandlesSequence 连续解码多帧不报错，帧长保持稳定
func TestReusedDecoderHandlesSequence(t *testing.T) {
	packet := testOpusPacket(t)

	dec, err := newOpusDecoder(asrSampleRate)
	if err != nil {
		t.Fatalf("创建解码器失败: %v", err)
	}
	out := make([]int16, opusMaxFrameSamples)

	for i := 0; i < 10; i++ {
		n, decErr := decodeOpusInto(dec, packet, out)
		if decErr != nil {
			t.Fatalf("第 %d 帧解码失败: %v", i, decErr)
		}
		if n != 320 {
			t.Fatalf("第 %d 帧采样数应为 320，实际 %d", i, n)
		}
	}
}
