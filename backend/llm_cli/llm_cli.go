package llm_cli

import (
	"context"
	"echat-backend/config"
	"echat-backend/global"
	llmpb "echat-backend/proto/llm"
	"echat-backend/tts_cli"
	"io"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var LLMServiceClient LLMService

type LLMService struct {
	client llmpb.LLMServiceClient
	In     chan *llmpb.LLMRequest  // 发送用户文本请求的通道
	Out    chan *llmpb.LLMResponse // 接收 LLM 回复的通道
}

// Init 初始化 LLM rpc客户端，并启动接收回复的协程和发送请求的协程
func Init() {
	addr := config.Get().LLM.GrpcAddr
	log.Printf("连接 LLM 服务: %s", addr)

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Printf("连接 LLM 服务失败: %v", err)
		return
	}

	LLMServiceClient.In = make(chan *llmpb.LLMRequest, 64)
	LLMServiceClient.Out = make(chan *llmpb.LLMResponse, 64)

	LLMServiceClient.client = llmpb.NewLLMServiceClient(conn)

	stream, err := LLMServiceClient.client.ChatStream(context.Background())
	if err != nil {
		log.Printf("打开 LLM 流失败: %v", err)
		return
	}

	// 接收 LLM 回复的协程
	go func() {
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				log.Printf("LLM 流已关闭")
				return
			}
			if err != nil {
				log.Printf("接收 LLM 回复错误: %v", err)
				return
			}
			log.Printf("[llm_cli] 收到回复: %s", resp)
			LLMServiceClient.Out <- resp
		}
	}()

	// 发送用户文本请求的协程
	go func() {
		for req := range LLMServiceClient.In {
			if err = stream.Send(req); err != nil {
				log.Printf("[llm_cli] 发送请求错误: %v", err)
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
