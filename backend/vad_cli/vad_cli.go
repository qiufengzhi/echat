package vad_cli

import (
	"context"
	"encoding/binary"
	"log"

	"echat-backend/config"

	vadpb "echat-backend/proto/vad"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var VADClientInstance *VADClient

// InitVADClient 从全局配置初始化 VAD 客户端，替代原有的 init() 硬编码地址。
// 连接失败时只记录日志不 panic，VAD 检测功能降级但不影响主流程。
func InitVADClient() {
	cfg := config.Get().VAD
	var err error
	VADClientInstance, err = newVADClient(cfg.GrpcAddr)
	if err != nil {
		log.Printf("[vad] 初始化客户端失败 (addr=%s): %v, VAD 功能已降级", cfg.GrpcAddr, err)
		return
	}
	log.Printf("[vad] 客户端已连接: %s", cfg.GrpcAddr)
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
