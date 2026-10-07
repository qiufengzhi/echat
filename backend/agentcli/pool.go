// Package agentcli 是 Go 后端调用 Python Agent 的客户端层
//
// 一轮对话一次 Generate RPC：请求一次，响应流式返回
// 取消通过取消该轮 context 表达，超时通过 RPC deadline 表达
// 连接按 session_id 分片到多条 ClientConn，把 TCP 连接级的丢包与流控耦合、
// 以及断连时的故障爆炸半径压到 1/N
package agentcli

import (
	"hash/fnv"
	"time"

	llmpb "echat-backend/proto/llm"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	keepaliveTime    = 10 * time.Second // 连接空闲时保活探测间隔
	keepaliveTimeout = 3 * time.Second  // 保活探测响应超时
)

// Pool 分片连接池：N 条 ClientConn 与对应 stub，按 sessionID 稳定取模
type Pool struct {
	stubs []llmpb.LLMServiceClient // 各分片的客户端 stub
	conns []*grpc.ClientConn       // 各分片的底层连接，Close 时统一关闭
}

// NewPool 建立分片连接池
//
// grpc.NewClient 是惰性建连，构造不阻塞启动，真正的连接在首次 RPC 时建立
// addr: Agent gRPC 服务地址
// shards: 分片数，小于 1 时按 1 处理
// return: 连接池实例，错误来自连接参数非法
func NewPool(addr string, shards int) (*Pool, error) {
	if shards < 1 {
		shards = 1
	}
	kp := keepalive.ClientParameters{
		Time:                keepaliveTime,
		Timeout:             keepaliveTimeout,
		PermitWithoutStream: true,
	}
	p := &Pool{
		stubs: make([]llmpb.LLMServiceClient, 0, shards),
		conns: make([]*grpc.ClientConn, 0, shards),
	}
	for i := 0; i < shards; i++ {
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithKeepaliveParams(kp),
			// otelgrpc：按 call 建 span 并逐轮传播 traceparent metadata（每轮一次 RPC 的前提）
			grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		)
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		p.conns = append(p.conns, conn)
		p.stubs = append(p.stubs, llmpb.NewLLMServiceClient(conn))
	}
	return p, nil
}

// Stub 取某会话所属分片的 stub
//
// sessionID: 会话 ID，同一会话恒定映射到同一分片
// return: 该分片的客户端 stub
func (p *Pool) Stub(sessionID string) llmpb.LLMServiceClient {
	return p.stubs[shardIndex(sessionID, len(p.stubs))]
}

// Next 取相邻分片的 stub，用于首选分片连接不可用时改投
//
// sessionID: 会话 ID
// return: 相邻分片的客户端 stub，只有一条分片时返回同一条
func (p *Pool) Next(sessionID string) llmpb.LLMServiceClient {
	return p.stubs[(shardIndex(sessionID, len(p.stubs))+1)%len(p.stubs)]
}

// Shards 返回分片数
//
// return: 分片数，至少为 1
func (p *Pool) Shards() int {
	return len(p.stubs)
}

// Close 关闭所有分片连接
//
// return: 首个出现的关闭错误，全部成功时返回 nil
func (p *Pool) Close() error {
	var firstErr error
	for _, conn := range p.conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// shardIndex 计算会话所属分片下标
//
// 用 FNV-1a 保证同一 sessionID 恒定落片，避免会话在分片间漂移
// sessionID: 会话 ID
// shards: 分片数，调用方保证大于 0
// return: 分片下标，取值 [0, shards)
func shardIndex(sessionID string, shards int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return int(h.Sum32() % uint32(shards))
}
