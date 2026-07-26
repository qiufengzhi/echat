package asr_cli

import (
	"context"
	"echat-backend/logging"
	"io"
	"os"

	asrpb "echat-backend/proto/asr"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var AsrServiceClient AsrService
var logger = logging.New("asr")

// AsrService 封装与 Python ASR gRPC 服务的双向流连接
type AsrService struct {
	client asrpb.AsrServiceClient
	In     chan asrpb.AudioChunk            // 发送音频数据的通道
	Out    chan *asrpb.TranscriptAudioChunk // 接收识别结果的通道
}

func init() {
	return
	addr := getEnv("ASR_ADDR", "127.0.0.1:50051")
	logger.Infow("连接 ASR 服务", "addr", addr)

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger.Fatalw("连接失败", "error", err)
	}

	AsrServiceClient.In = make(chan asrpb.AudioChunk, 64)
	AsrServiceClient.Out = make(chan *asrpb.TranscriptAudioChunk, 64)

	AsrServiceClient.client = asrpb.NewAsrServiceClient(conn)

	// 打开双向流
	stream, err := AsrServiceClient.client.RecognizeAudioStream(context.Background())
	if err != nil {
		logger.Fatalw("打开流失败", "error", err)
	}

	// 接收协程：持续从 ASR 服务读取识别结果
	go func() {
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				logger.Warnw("接收错误", "error", err)
				return
			}
			AsrServiceClient.Out <- resp
		}
	}()

	// 发送协程：从 In 通道取音频块发给 ASR 服务
	go func() {
		for audio := range AsrServiceClient.In {
			if err = stream.Send(&audio); err != nil {
				logger.Warnw("发送错误", "error", err)
				break
			}
		}
	}()
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
