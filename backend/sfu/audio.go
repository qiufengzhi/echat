package sfu

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/opus"
)

// ---------- Opus 编码器接口 ----------

// OpusEncoder 将 PCM16 采样编码为 Opus 字节
// 通过 build tags 在 CGo 和非 CGo 环境下提供不同实现
type OpusEncoder interface {
	// Encode 将单声道 int16 PCM 采样编码为 Opus 字节，返回编码后的 payload
	Encode(pcm []int16) ([]byte, error)
	// Close 释放编码器资源
	Close() error
	// SetBitrate 设置编码码率（bps）
	SetBitrate(bps int) error
	// SetVBR 开关可变码率
	SetVBR(enabled bool) error
	// SetComplexity 设置编码复杂度（1-10）
	SetComplexity(complexity int) error
	// SetDTX 开关非连续传输（静音时不发送数据）
	SetDTX(enabled bool) error
}

// newOpusEncoder 创建 Opus 编码器，由 build-tag 文件提供具体实现
func newOpusEncoder() (OpusEncoder, error) {
	return newOpusEncoderImpl()
}

// newOpusEncoderPreset 创建 Opus 编码器并预设参数，确保输出质量和兼容性
func newOpusEncoderPreset() (OpusEncoder, error) {
	enc, err := newOpusEncoderImpl()
	if err != nil {
		return nil, err
	}
	// CBR 32kbps + 复杂度 10（RFC 6716 推荐全频带语音 28-40kbps）
	// 64kbps VBR 模式下 Chrome NetEq 抖动缓冲区会异常丢弃导致播放不完整，回退到已验证可用的配置
	if err := enc.SetBitrate(32000); err != nil {
		enc.Close()
		return nil, fmt.Errorf("设置 Opus 码率失败: %w", err)
	}
	if err := enc.SetVBR(false); err != nil {
		enc.Close()
		return nil, fmt.Errorf("设置 Opus VBR 失败: %w", err)
	}
	if err := enc.SetComplexity(10); err != nil {
		enc.Close()
		return nil, fmt.Errorf("设置 Opus 复杂度失败: %w", err)
	}
	// 关闭 DTX，避免静音检测误触发导致语音首尾丢失
	if err := enc.SetDTX(false); err != nil {
		enc.Close()
		return nil, fmt.Errorf("设置 Opus DTX 失败: %w", err)
	}
	return enc, nil
}

// ---------- 常量 ----------

// opusFrameSamples 单个 Opus 帧的采样数：20ms @ 16kHz = 320 samples
const opusFrameSamples = 320

// opusEncoderFrameSamples Opus 编码器输入帧采样数：20ms @ 48kHz = 960 samples
const opusEncoderFrameSamples = 960

// upsample16kTo48k 已废弃 — 替换为 gunter-q12/resample polyphase FIR 重采样库
// 重采样逻辑在 sfu.go 的 aiCall 协程中通过 resample.New + Write 实现

// asrSampleRate 送云端识别的采样率，与 opus 解码输出一致
const asrSampleRate = 16000

// opusMaxFrameSamples 一帧 Opus 解码后的最大采样数：120ms @16kHz = 1920
const opusMaxFrameSamples = 320 * 6

// newOpusDecoder 创建送识别用的 Opus 解码器
//
// sampleRate: 解码输出采样率，取 asrSampleRate
// return: 解码器指针，创建失败时返回错误
func newOpusDecoder(sampleRate int) (*opus.Decoder, error) {
	dec, err := opus.NewDecoderWithOutput(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("创建解码器失败: %w", err)
	}
	return &dec, nil
}

// decodeOpusInto 用已有解码器把 Opus 数据解码进调用方提供的缓冲
//
// 解码器与输出缓冲都由调用方复用：每包新建解码器、每包分配输出切片，
// 是本路径上最容易忽略的两笔开销
// dec: 复用解码器，必须只被一个协程使用
// opusData: RTP payload；长度不足 1 字节时视为 DTX 静音包
// out: 输出缓冲，容量需不小于 opusMaxFrameSamples
// return: 解出的采样数与错误，DTX 静音包返回 0
func decodeOpusInto(dec *opus.Decoder, opusData []byte, out []int16) (int, error) {
	if len(opusData) < 1 {
		return 0, nil
	}
	n, err := dec.DecodeToInt16(opusData, out)
	if err != nil {
		return 0, fmt.Errorf("解码失败: %w", err)
	}
	return n, nil
}

// int16ToLEBytes 将 []int16 转为小端序字节切片，供 ASR 服务使用
func int16ToLEBytes(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	return buf
}

// leBytesToInt16 将小端序 PCM16 字节切片转为 []int16 采样
// 是 int16ToLEBytes 的逆操作，供 TTS 合成输出编码为 Opus 时使用
func leBytesToInt16(data []byte) []int16 {
	samples := make([]int16, len(data)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return samples
}
