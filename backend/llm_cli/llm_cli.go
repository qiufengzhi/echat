package llm_cli

import (
	"context"
	"echat-backend/config"
	"echat-backend/global"
	"echat-backend/logging"
	llmpb "echat-backend/proto/llm"
	"echat-backend/tts_cli"
	"io"
	"time"

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

// Init 初始化 LLM 通道和消费协程，后台异步连接并自动重试，不阻塞主进程
func Init() {
	addr := config.Get().LLM.GrpcAddr

	LLMServiceClient.In = make(chan *llmpb.LLMRequest, 64)
	LLMServiceClient.Out = make(chan *llmpb.LLMResponse, 64)

	// TTS 消费协程，复用 Out 通道生命周期
	go func() {
		for resp := range LLMServiceClient.Out {
			// 该房间 AI 已离线时不播放回复，待机/在线则正常播放
			if global.AIStates.Get(resp.RoomId) == global.AIOffline {
				continue
			}
			tts_cli.GlobalTTSService.ProcessText(resp)
		}
	}()

	// 后台异步连接，失败自动重试
	go connectAndServe(addr)
}

// connectAndServe 循环连接 LLM，连接成功后等待流断开再重连
func connectAndServe(addr string) {
	for {
		done := make(chan struct{})
		if connectLLM(addr, done) {
			<-done // 阻塞直到流断开
			logger.Warnw("LLM 流断开，准备重连", "addr", addr)
		}
		time.Sleep(3 * time.Second)
	}
}

// connectLLM 尝试连接 LLM gRPC 服务并建立双向流，成功返回 true，并在流断开时 close(done)
func connectLLM(addr string, done chan struct{}) bool {
	logger.Infow("尝试连接 LLM 服务", "addr", addr)

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger.Warnw("连接 LLM 服务失败", "error", err)
		return false
	}

	LLMServiceClient.client = llmpb.NewLLMServiceClient(conn)

	stream, err := LLMServiceClient.client.ChatStream(context.Background())
	if err != nil {
		logger.Warnw("打开 LLM 流失败", "error", err)
		return false
	}

	// 接收 LLM 回复的协程，退出时通知 done
	go func() {
		defer close(done)
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
			if err := stream.Send(req); err != nil {
				logger.Warnw("发送请求错误", "error", err)
				break
			}
		}
	}()

	logger.Infow("LLM 服务连接成功", "addr", addr)
	return true
}
