package sfu

import (
	"fmt"

	"github.com/pion/opus"
)

// minOpusPacketLen Opus RTP payload 最小长度：至少需要一个 TOC 字节（Table of Contents header）。
// 低于此长度的包通常是 DTX（不连续传输）静音包或舒适噪声包，无需解码即可安全跳过。
const minOpusPacketLen = 1

// decodeOpusToInt16 将 Opus 编码的 RTP payload 解码为 PCM16 采样。
// 解码器为单声道，sampleRate 通常为 16000。
// 如果 payload 过短（DTX 静音包等），返回空切片而非错误，避免日志噪声。
func decodeOpusToInt16(opusData []byte, sampleRate int) ([]int16, error) {
	// DTX / 舒适噪声包：payload 太短无法包含 TOC，直接返回空 PCM
	if len(opusData) < minOpusPacketLen {
		return []int16{}, nil
	}

	dec, err := opus.NewDecoderWithOutput(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("创建解码器失败: %w", err)
	}

	// 一帧最大 120ms @16kHz = 1920 samples，320*6 兜住大帧
	pcm := make([]int16, 320*6)
	n, err := dec.DecodeToInt16(opusData, pcm)
	if err != nil {
		return nil, fmt.Errorf("解码失败: %w", err)
	}

	return pcm[:n], nil
}

// int16ToLEBytes 将 []int16 转为小端序字节切片，供 ASR 服务使用。
func int16ToLEBytes(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, v := range samples {
		buf[i*2] = byte(v)
		buf[i*2+1] = byte(v >> 8)
	}
	return buf
}
