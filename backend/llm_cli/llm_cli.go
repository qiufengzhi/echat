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
	"google.golang.org/grpc/keepalive"
)

var LLMServiceClient LLMService
var logger = logging.New("llm")

type LLMService struct {
	client llmpb.LLMServiceClient  // gRPC 客户端实例
	conn   *grpc.ClientConn        // gRPC 底层连接，重连或退出时关闭
	cancel context.CancelFunc      // 当前流上下文的取消函数，用于通知协程退出
	In     chan *llmpb.LLMRequest  // 发送用户文本请求的通道
	Out    chan *llmpb.LLMResponse // 接收 LLM 回复的通道
}

// Init 初始化 LLM 通道和消费协程，后台异步连接并自动重试，不阻塞主进程
func Init() {
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
	go connectAndServe(config.Get().LLM.GrpcAddr)
}

// connectAndServe 循环连接 LLM，连接成功后等待流断开再重连
func connectAndServe(addr string) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		done := make(chan struct{})
		if connectLLM(addr, done) {
			<-done // 阻塞直到流断开
			logger.Warnw("LLM 流断开，准备重连", "addr", addr)
			backoff = time.Second // 成功后重置退避
		}
		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// connectLLM 尝试连接 LLM gRPC 服务并建立双向流，成功返回 true，并在流断开时 close(done)
func connectLLM(addr string, done chan struct{}) bool {
	logger.Infow("尝试连接 LLM 服务", "addr", addr)

	// 清理旧连接和旧协程，避免 goroutine 与 fd 泄漏
	if LLMServiceClient.cancel != nil {
		LLMServiceClient.cancel()
	}
	if LLMServiceClient.conn != nil {
		_ = LLMServiceClient.conn.Close()
	}

	kp := keepalive.ClientParameters{
		Time:                10 * time.Second, // 连接空闲时多久发一次保活探测
		Timeout:             3 * time.Second,  // 等待探测响应的超时时间，超则认为连接已断
		PermitWithoutStream: true,             // 即使没有活跃流也发送保活探测
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(kp),
	)
	if err != nil {
		logger.Warnw("连接 LLM 服务失败", "error", err)
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := llmpb.NewLLMServiceClient(conn)
	stream, err := client.ChatStream(ctx)
	if err != nil {
		cancel()
		_ = conn.Close()
		logger.Warnw("打开 LLM 流失败", "error", err)
		return false
	}

	LLMServiceClient.client = client
	LLMServiceClient.conn = conn
	LLMServiceClient.cancel = cancel

	// 接收 LLM 回复的协程，退出时通知 done
	go func() {
		defer close(done)
		defer cancel()
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
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-LLMServiceClient.In:
				if !ok {
					return
				}
				if err := stream.Send(req); err != nil {
					logger.Warnw("发送请求错误", "error", err)
					return
				}
			}
		}
	}()

	logger.Infow("LLM 服务连接成功", "addr", addr)
	return true
}
