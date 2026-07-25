//go:build !opus_native

// Package sfu WASM Opus 编码器（真实 libopus 编译为 WASM，通过 wazero 运行）
// 编码质量等同于原生 libopus，支持 VBR，无需 CGo/opus-dev
// 与 ccgo 转译版不同：WASM 运行的是未修改的原生 libopus 二进制，无已知缺陷

package sfu

import (
	wasmOpus "github.com/jj11hh/opus"
)

// opusEncoder 封装 jj11hh/opus（WASM 原生 libopus）的 Opus 编码器
type opusEncoder struct {
	enc *wasmOpus.Encoder
}

func newOpusEncoderImpl() (OpusEncoder, error) {
	enc, err := wasmOpus.NewEncoder(48000, 1, wasmOpus.AppVoIP)
	if err != nil {
		return nil, err
	}
	return &opusEncoder{enc: enc}, nil
}

func (e *opusEncoder) Encode(pcm []int16) ([]byte, error) {
	buf := make([]byte, 4096)
	n, err := e.enc.Encode(pcm, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (e *opusEncoder) SetBitrate(bps int) error {
	return e.enc.SetBitrate(bps)
}

func (e *opusEncoder) SetVBR(enabled bool) error {
	return e.enc.SetVBR(enabled)
}

func (e *opusEncoder) SetComplexity(complexity int) error {
	return e.enc.SetComplexity(complexity)
}

func (e *opusEncoder) SetDTX(enabled bool) error {
	return e.enc.SetDTX(enabled)
}

func (e *opusEncoder) Close() error {
	// WASM encoder 无显式清理，由 wazero runtime 管理生命周期
	return nil
}
