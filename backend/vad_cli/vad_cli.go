package vad_cli

import (
	"context"
	"encoding/binary"
	"log"

	vadpb "echat-backend/proto/vad"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var VADClientInstance *VADClient

func init() {
	var err error
	VADClientInstance, err = newVADClient("127.0.0.1:50052")
	if err != nil {
		log.Printf("初始化vad客户端失败: %v", err)
		return
	}
}

type VADClient struct {
	conn   *grpc.ClientConn
	client vadpb.VadServiceClient
	ctx    context.Context
}

// newVADClient 连接到 Python VAD gRPC 服务
func newVADClient(addr string) (*VADClient, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}
	return &VADClient{
		conn:   conn,
		client: vadpb.NewVadServiceClient(conn),
		ctx:    context.Background(),
	}, nil
}

// Close 关闭客户端连接
func (c *VADClient) Close() error {
	return c.conn.Close()
}

// Detect 检测一段 int16 PCM 数据是否包含人声
func (c *VADClient) Detect(pcm []int16, sampleRate int32) (speech bool, score float32, err error) {
	// int16 转 little-endian 字节
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}

	resp, err := c.client.VadDetect(c.ctx, &vadpb.VadRequest{
		Pcm:        buf,
		SampleRate: sampleRate,
	})
	if err != nil {
		return false, 0, err
	}
	return resp.Speech, resp.Score, nil
}
