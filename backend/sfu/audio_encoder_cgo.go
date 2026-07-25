//go:build opus_native

// Package sfu 原生 libopus 编码器（CGo 链接系统 libopus，编码质量最佳）
// 编译方式: CGO_ENABLED=1 go build -tags opus_native .
// 需预装: opus-dev (Alpine) / libopus-dev (Debian) / libopus (其他)

package sfu

/*
#cgo LDFLAGS: -lopus
#cgo linux LDFLAGS: -lopus
#cgo darwin LDFLAGS: -lopus
#cgo windows LDFLAGS: -lopus

#include <opus/opus.h>

// 辅助函数：封装 opus_encoder_create
static inline OpusEncoder* create_encoder(int sample_rate, int channels, int application, int *err) {
	return opus_encoder_create(sample_rate, channels, application, err);
}

// 辅助函数：封装 opus_encode
static inline int encode_pcm(OpusEncoder *enc, const opus_int16 *pcm, int frame_size, unsigned char *data, int max_data_bytes) {
	return opus_encode(enc, pcm, frame_size, data, max_data_bytes);
}

// 辅助函数：设置编码器参数
static inline int set_bitrate(OpusEncoder *enc, int bitrate) {
	return opus_encoder_ctl(enc, OPUS_SET_BITRATE(bitrate));
}
static inline int set_vbr(OpusEncoder *enc, int vbr) {
	return opus_encoder_ctl(enc, OPUS_SET_VBR(vbr));
}
static inline int set_complexity(OpusEncoder *enc, int complexity) {
	return opus_encoder_ctl(enc, OPUS_SET_COMPLEXITY(complexity));
}
static inline int set_dtx(OpusEncoder *enc, int dtx) {
		return opus_encoder_ctl(enc, OPUS_SET_DTX(dtx));
	}
static inline void destroy_encoder(OpusEncoder *enc) {
	opus_encoder_destroy(enc);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// opusEncoder CGo 原生 libopus 编码器
type opusEncoder struct {
	enc *C.OpusEncoder
	sr  int
	ch  int
}

func newOpusEncoderImpl() (OpusEncoder, error) {
	var errCode C.int
	enc := C.create_encoder(48000, 1, C.OPUS_APPLICATION_VOIP, &errCode)
	if errCode != C.OPUS_OK {
		return nil, fmt.Errorf("创建 Opus 编码器失败: %d", int(errCode))
	}
	return &opusEncoder{enc: enc, sr: 48000, ch: 1}, nil
}

func (e *opusEncoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) == 0 {
		return nil, errors.New("PCM 数据为空")
	}

	buf := make([]byte, 4096)
	var pcmPtr *C.opus_int16
	if len(pcm) > 0 {
		pcmPtr = (*C.opus_int16)(unsafe.Pointer(&pcm[0]))
	}

	n := C.encode_pcm(e.enc, pcmPtr, C.int(len(pcm)), (*C.uchar)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n < 0 {
		return nil, fmt.Errorf("Opus 编码失败: %d", int(n))
	}
	return buf[:n], nil
}

func (e *opusEncoder) SetBitrate(bps int) error {
	ret := C.set_bitrate(e.enc, C.int(bps))
	if ret != C.OPUS_OK {
		return fmt.Errorf("设置码率失败: %d", int(ret))
	}
	return nil
}

func (e *opusEncoder) SetVBR(enabled bool) error {
	v := C.int(0)
	if enabled {
		v = 1
	}
	ret := C.set_vbr(e.enc, v)
	if ret != C.OPUS_OK {
		return fmt.Errorf("设置 VBR 失败: %d", int(ret))
	}
	return nil
}

func (e *opusEncoder) SetComplexity(complexity int) error {
	ret := C.set_complexity(e.enc, C.int(complexity))
	if ret != C.OPUS_OK {
		return fmt.Errorf("设置复杂度失败: %d", int(ret))
	}
	return nil
}

func (e *opusEncoder) SetDTX(enabled bool) error {
	v := C.int(0)
	if enabled {
		v = 1
	}
	ret := C.set_dtx(e.enc, v)
	if ret != C.OPUS_OK {
		return fmt.Errorf("设置 DTX 失败: %d", int(ret))
	}
	return nil
}

func (e *opusEncoder) Close() error {
	if e.enc != nil {
		C.destroy_encoder(e.enc)
		e.enc = nil
	}
	return nil
}
