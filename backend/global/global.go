package global

import "sync/atomic"

// StartAiAssistant 是否启动 AI 助手服务, 默认为 false。因为需要语音唤醒，因此ASR不能停，当前打断的是 ASR-->LLM  LLM-->TTS
var StartAiAssistant atomic.Bool
