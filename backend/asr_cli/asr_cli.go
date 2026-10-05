// Package asr_cli 封装云端实时语音识别
//
// 本文件是包对外的入口：识别器装配、音频投递与结果读取，具体实现见 aliyun_asr.go
// 输入输出都经这里的函数出入，调用方不直接触碰识别器内部通道
package asr_cli

import (
	"echat-backend/dataplane"
	"echat-backend/logging"
	asrpb "echat-backend/proto/asr"
)

// logger asr 包的具名日志器
var logger = logging.New("asr")

// inputDropped 累计丢弃的待识别音频块
//
// 识别器未启用、或输入队列已满时丢弃：语音是「最新最有价值」的流，
// 陈旧的音频块晚到只会让识别文本错位
var inputDropped = dataplane.NewCounter("asr.input", 0)

// disabledResults 识别器未启用时返回的空结果通道
//
// 永不产出也不会被关闭：结果消费方可以照常 range，既不 panic 也不空转
var disabledResults = make(chan *asrpb.TranscriptAudioChunk)

// Enabled 返回识别器是否已启用
//
// provider 配置为空时 Init 不构造识别器，此时为未启用
// return: 已启用返回 true
func Enabled() bool {
	return GlobalRecognizer != nil
}

// Push 投递一个音频块给云端识别，永不阻塞
//
// 拒绝投递的两种情况都计入丢弃计数：识别器未启用、输入队列已满
// chunk: 待识别的音频块，SessionId 为空时会被识别器跳过
// return: 已投递返回 true
func Push(chunk asrpb.AudioChunk) bool {
	if GlobalRecognizer == nil {
		inputDropped.Drop("reason", "recognizer_disabled", "sessionID", chunk.SessionId)
		return false
	}

	select {
	case GlobalRecognizer.AudioIn <- chunk:
		return true
	default:
		inputDropped.Drop("reason", "input_full", "sessionID", chunk.SessionId)
		return false
	}
}

// Results 返回识别结果通道
//
// 识别器未启用时返回一个永不产出的通道，调用方无需判空
// return: 识别结果只读通道
func Results() <-chan *asrpb.TranscriptAudioChunk {
	if GlobalRecognizer == nil {
		return disabledResults
	}
	return GlobalRecognizer.AudioOut
}
