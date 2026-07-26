package llm_cli

import (
	"context"
	"echat-backend/config"
	"echat-backend/global"
	"echat-backend/logging"
	llmpb "echat-backend/proto/llm"
	"echat-backend/tts_cli"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var LLMServiceClient LLMService
var logger = logging.New("llm")

type LLMService struct {
	client llmpb.LLMServiceClient
	In     chan *llmpb.LLMRequest  // 发送用户文本请求的通道
	Out    chan *llmpb.LLMResponse // 接收 LLM 回复的通道
}

// Init 初始化 LLM rpc客户端，并启动接收回复的协程和发送请求的协程
func Init() {
	addr := config.Get().LLM.GrpcAddr
	logger.Infow("连接 LLM 服务", "addr", addr)

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger.Warnw("连接 LLM 服务失败", "error", err)
		return
	}

	LLMServiceClient.In = make(chan *llmpb.LLMRequest, 64)
	LLMServiceClient.Out = make(chan *llmpb.LLMResponse, 64)

	LLMServiceClient.client = llmpb.NewLLMServiceClient(conn)

	stream, err := LLMServiceClient.client.ChatStream(context.Background())
	if err != nil {
		logger.Warnw("打开 LLM 流失败", "error", err)
		return
	}

	// 接收 LLM 回复的协程
	go func() {
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				logger.Infow("LLM 流已关闭")
				return
			}
			if err != nil {
				logger.Warnw("接收 LLM 回复错误", "error", err)
				return
			}
			logger.Debugw("收到回复", "response", resp)
			LLMServiceClient.Out <- resp
		}
	}()

	// 发送用户文本请求的协程
	go func() {
		for req := range LLMServiceClient.In {
			if err = stream.Send(req); err != nil {
				logger.Warnw("发送请求错误", "error", err)
				break
			}
		}
	}()

	// 启动 TTS 消费协程
	go func() {
		for resp := range LLMServiceClient.Out {
			// 是否开启ai语音助手
			if !global.StartAiAssistant.Load() {
				continue
			}
			// 给 TTS 发送语音合成请求
			tts_cli.GlobalTTSService.ProcessText(resp)
		}
	}()
}
