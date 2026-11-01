package sfu

// 本文件是「音频热路径」的 Go 原生基准（benchmark），只在 go test 时参与编译，
// 不会进入生产二进制，也不引入任何第三方测试框架。
//
// 为什么要放在 sfu 包内部（而不是单独的 bench 包）：
//   - 本文件声明 package sfu，和被测代码同包，因此可以直接调用未导出函数
//     （decodeOpusToInt16 / int16ToLEBytes 等），不必为了测试把它们改成导出；
//   - 保持「被测代码在哪，基准就在哪」的就近原则，重构时不容易漏改。
//
// 运行方式（在 backend 目录下）：
//   go test -run=^$ -bench=. -benchmem ./sfu/
//     -run=^$       只跑基准，不跑单元测试（^$ 匹配不到任何测试名）
//     -bench=.      跑所有 Benchmark 函数
//     -benchmem     额外输出每次操作的内存分配次数与字节数（定位 GC 压力的关键）
//
// 常用补充参数：
//   -benchtime=3s     每个基准至少跑 3 秒（默认 1s，样本少时抖动大）
//   -count=6          每个基准重复 6 轮，配合 benchstat 做统计对比
//   -cpu=1,4,8        分别在不同 GOMAXPROCS 下跑，观察并发扩展性
//
// 定位热点（基准只能告诉你「慢」，profile 才能告诉你「慢在哪」）：
//   go test -run=^$ -bench=BenchmarkDecodeOpusToInt16 -cpuprofile cpu.out ./sfu/
//   go tool pprof -http=:0 cpu.out

import (
	"bytes"
	"math"
	"testing"

	"github.com/gunter-q12/resample"
	"github.com/pion/opus"
)

// ---------- 测试素材（fixture）构造 ----------
//
// 基准最怕「不可复现」：如果每次跑的数据都不一样，前后两次的数字就无法对比。
// 这里用确定性的正弦波（固定频率、固定幅值、不引入随机数）生成 PCM，
// 保证同一台机器上每次运行输入完全一致，数字变化只反映代码变化。

// benchSinePCM 生成 n 个采样点的单声道正弦波 PCM16
// n 采样点数，freqHz 正弦频率，sampleRate 采样率
// 幅值取 12000（满量程 32767 的约 1/3），留足 headroom 避免叠加溢出
func benchSinePCM(n int, freqHz float64, sampleRate int) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		v := math.Sin(2 * math.Pi * freqHz * float64(i) / float64(sampleRate))
		pcm[i] = int16(v * 12000)
	}
	return pcm
}

// benchOpusPayload 用生产环境同一套预设编码器，把一帧 20ms@48kHz PCM 编成 Opus payload
// 作为解码类基准的输入素材。用真实编码器产出，而不是硬编码一段字节，
// 好处是：编码器参数一旦变更（码率/复杂度），素材自动跟着变，基准不会测到陈旧数据。
func benchOpusPayload(b *testing.B) []byte {
	b.Helper()
	enc, err := newOpusEncoderPreset()
	if err != nil {
		// 本机没有 libopus 或未开 CGO 时跳过，而不是 fail：
		// 基准是度量工具，不该因为环境缺依赖就让 CI 变红
		b.Skipf("Opus 编码器不可用（缺少 libopus 或 CGO 未启用）: %v", err)
	}
	defer enc.Close()

	payload, err := enc.Encode(benchSinePCM(opusEncoderFrameSamples, 440, 48000))
	if err != nil {
		b.Fatalf("生成测试素材失败: %v", err)
	}
	return payload
}

// ---------- 解码路径 ----------

// BenchmarkDecodeOpusToInt16 度量当前生产实现：每收到一个 RTP 包解码一次 Opus。
//
// 这是 deliverToASR 里「每包必走」的路径（sfu.go 的 deliverToASR 循环），
// 20ms 一包意味着单个房间单路音频每秒就要调用 50 次；
// 100 路并发 = 每秒 5000 次。它的单次成本直接决定单机能扛多少路。
//
// 当前生产实现：解码器每发布者创建一次（newOpusDecoder），循环内复用并逐包
// decodeOpusInto——本基准与生产代码保持同构（解码器/缓冲区在循环外创建）。
// 「每包新建解码器」的历史写法保留在下方 BenchmarkDecodeOpusNewDecoderPerCall
// 作为对照组，两个数字之差即该项优化的收益依据。
func BenchmarkDecodeOpusToInt16(b *testing.B) {
	payload := benchOpusPayload(b)
	// SetBytes 让 benchstat 额外输出 MB/s 吞吐，便于跨参数横向比较
	b.SetBytes(int64(len(payload)))
	b.ReportMetric(float64(len(payload)), "payload-B/op")

	// 与生产代码同构：deliverToASR 中解码器与输出缓冲各创建一次、循环内复用
	dec, err := newOpusDecoder(asrSampleRate)
	if err != nil {
		b.Fatalf("创建解码器失败: %v", err)
	}
	buf := make([]int16, opusMaxFrameSamples)

	// b.Loop() 是 Go 1.24+ 的推荐写法：框架自动控制迭代次数，
	// 并且能保证循环体不会被编译器整体优化掉（比手写 for i := 0; i < b.N; i++ 更可靠）
	for b.Loop() {
		n, err := decodeOpusInto(dec, payload, buf)
		if err != nil {
			b.Fatalf("解码失败: %v", err)
		}
		if n == 0 {
			b.Fatal("解码结果为空，素材可能无效")
		}
	}
}

// BenchmarkDecodeOpusNewDecoderPerCall 对照组：每个 RTP 包新建一次解码器。
//
// 量化「每包新建解码器」相对生产实现（复用解码器，见上）的额外代价：
// 除解码本身外，额外计入每包一次的 opus.NewDecoderWithOutput 分配与初始化。
// 若该数字与复用版接近，说明解码器创建不是热点，重构优先级可以下调。
func BenchmarkDecodeOpusNewDecoderPerCall(b *testing.B) {
	payload := benchOpusPayload(b)
	buf := make([]int16, opusMaxFrameSamples)

	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		dec, err := opus.NewDecoderWithOutput(asrSampleRate, 1)
		if err != nil {
			b.Fatalf("创建解码器失败: %v", err)
		}
		if _, err := dec.DecodeToInt16(payload, buf); err != nil {
			b.Fatalf("解码失败: %v", err)
		}
	}
}

// ---------- 编码路径 ----------

// BenchmarkOpusEncodeFrame48k 度量 TTS 回灌链路的编码成本。
// runAITTSLoop 里每一帧 20ms PCM 都要编码成 Opus 再走 RTP 发出，
// 编码是这条链路上最重的 CPU 开销（复杂度固定为 10）。
func BenchmarkOpusEncodeFrame48k(b *testing.B) {
	enc, err := newOpusEncoderPreset()
	if err != nil {
		b.Skipf("Opus 编码器不可用: %v", err)
	}
	defer enc.Close()

	pcm := benchSinePCM(opusEncoderFrameSamples, 440, 48000)
	b.SetBytes(int64(len(pcm) * 2)) // 输入 PCM 字节数

	for b.Loop() {
		if _, err := enc.Encode(pcm); err != nil {
			b.Fatalf("编码失败: %v", err)
		}
	}
}

// ---------- 采样格式转换 ----------

// BenchmarkInt16ToLEBytes 度量 PCM16 → 小端字节流的转换（送 ASR 前必走）。
// 每个 RTP 包调用一次，属于「小而高频」的典型：单次很便宜，但乘以每秒几千次就值得看一眼。
func BenchmarkInt16ToLEBytes(b *testing.B) {
	pcm := benchSinePCM(320, 440, 16000) // 20ms @16kHz
	b.SetBytes(int64(len(pcm) * 2))
	for b.Loop() {
		_ = int16ToLEBytes(pcm)
	}
}

// BenchmarkLEBytesToInt16 上一项的逆操作，TTS 合成输出编码为 Opus 前使用。
func BenchmarkLEBytesToInt16(b *testing.B) {
	raw := int16ToLEBytes(benchSinePCM(320, 440, 16000))
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		_ = leBytesToInt16(raw)
	}
}

// ---------- 重采样 ----------

// BenchmarkResample16kTo48k 度量 TTS 链路的 16kHz→48kHz 重采样。
//
// 这里刻意复刻了生产写法：每次都 resample.New() 新建重采样器。
// runAITTSLoop 目前是每个 session 建一次（可接受），
// 但循环内还有一处 newResampler（sfu.go 约 859 行），若它落在每句/每帧粒度，
// 这个基准的分配数字会立刻暴露问题。
func BenchmarkResample16kTo48k(b *testing.B) {
	// 200ms @16kHz：与生产的 rawResampleThreshold(6400B) 同量级
	raw := int16ToLEBytes(benchSinePCM(3200, 440, 16000))
	var out bytes.Buffer

	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		out.Reset()
		r, err := resample.New(&out, resample.FormatInt16, 16000, 48000, 1, resample.WithKaiserFastFilter())
		if err != nil {
			b.Fatalf("创建重采样器失败: %v", err)
		}
		if _, err := r.Write(raw); err != nil {
			b.Fatalf("重采样失败: %v", err)
		}
	}
}
