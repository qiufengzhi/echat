package agentcli

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"echat-backend/logging"
	"echat-backend/observability"
	llmpb "echat-backend/proto/llm"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var logger = logging.New("agentcli")

const (
	defaultTurnTimeout  = 30 * time.Second // 未配置时的单轮 RPC 总超时
	closeSessionTimeout = 3 * time.Second  // 会话释放 RPC 的超时
	supersedeWait       = time.Second      // 抢占时等待旧轮释放在途登记的上限
)

// Options 客户端可调参数
type Options struct {
	Addr        string                                     // Agent gRPC 服务地址
	Shards      int                                        // 连接分片数，小于 1 时按 1 处理
	TurnTimeout time.Duration                              // 单轮 RPC 总超时，小于等于 0 时取默认值
	Dispatch    func(context.Context, *llmpb.TurnResponse) // 响应落地回调，携带该轮 ctx，每事件一次，nil 表示丢弃
}

// Stats 客户端运行计数，用于观测与排查
type Stats struct {
	Sent      int64 // 已发起的轮次数
	Responses int64 // 已分发的响应事件数
	Cancelled int64 // 被主动取消的轮次数
	Preempted int64 // 因同会话新轮次抢占而被取消的轮次数
	Rejected  int64 // 在途轮次未及时释放而被放弃的轮次数
	TimedOut  int64 // 因超时结束的轮次数
	Failed    int64 // 其他错误结束的轮次数
	Retried   int64 // 改投相邻分片重试的次数
}

// Client 每轮一次 RPC 的 Agent 客户端
type Client struct {
	pool        *Pool                                      // 分片连接池
	reg         *Registry                                  // 每会话在途轮次表
	dispatch    func(context.Context, *llmpb.TurnResponse) // 响应落地回调
	turnTimeout time.Duration                              // 单轮 RPC 总超时

	sent      int64 // 计数：已发起轮次
	responses int64 // 计数：已分发响应
	cancelled int64 // 计数：主动取消
	preempted int64 // 计数：被新轮次抢占
	rejected  int64 // 计数：在途未释放被放弃
	timedOut  int64 // 计数：超时
	failed    int64 // 计数：其他错误
	retried   int64 // 计数：改投分片
}

// New 创建客户端并建立分片连接池
//
// opts: 地址、分片数、单轮超时与响应回调，Dispatch 为 nil 时丢弃响应
// return: 客户端实例，错误来自连接参数非法
func New(opts Options) (*Client, error) {
	timeout := opts.TurnTimeout
	if timeout <= 0 {
		timeout = defaultTurnTimeout
	}
	pool, err := NewPool(opts.Addr, opts.Shards)
	if err != nil {
		return nil, err
	}
	dispatch := opts.Dispatch
	if dispatch == nil {
		dispatch = func(context.Context, *llmpb.TurnResponse) {}
	}
	return &Client{
		pool:        pool,
		reg:         NewRegistry(),
		dispatch:    dispatch,
		turnTimeout: timeout,
	}, nil
}

// SendTurn 在给定 ctx 下发起一轮对话，立即返回，响应经 Dispatch 回调异步送达
//
// ctx 携带 turn 根 span：随 RPC 传播到 Python，供 otelgrpc 建跨语言 trace
// 同一会话已有在途轮次时先取消它再接手：用户再次开口即打断上一轮
// 取消后旧轮需要一点时间释放在途登记，所以接手在新协程内异步完成，不阻塞调用方
// ctx: 该轮的父上下文（turn span）
// req: 轮次请求，SessionId 与 TurnId 必填
// return: 入参非法时返回错误
func (c *Client) SendTurn(ctx context.Context, req *llmpb.TurnRequest) error {
	if req.SessionId == "" || req.TurnId == "" {
		return errors.New("agentcli: session_id 与 turn_id 均不能为空")
	}
	go c.startTurn(ctx, req)
	return nil
}

// startTurn 抢占同会话在途轮次后发起本轮
//
// parent: 该轮父上下文（turn span）
// req: 轮次请求
func (c *Client) startTurn(parent context.Context, req *llmpb.TurnRequest) {
	if c.reg.Cancel(req.SessionId) {
		atomic.AddInt64(&c.preempted, 1)
		observability.IncAgentPreempted()
		logger.InfowCtx(parent, "同会话已有在途轮次，先取消再发起新轮", "sessionID", req.SessionId, "turnID", req.TurnId)
		c.waitSlot(req.SessionId)
	}
	ctx, cancel := context.WithTimeout(parent, c.turnTimeout)
	if err := c.reg.Begin(req.SessionId, req.TurnId, cancel); err != nil {
		cancel()
		atomic.AddInt64(&c.rejected, 1)
		observability.IncAgentRejected()
		logger.Warnw("在途轮次未及时释放，放弃本轮", "sessionID", req.SessionId, "turnID", req.TurnId)
		return
	}
	atomic.AddInt64(&c.sent, 1)
	c.runTurn(ctx, cancel, req)
}

// waitSlot 等待会话的在途轮次登记释放，上限 supersedeWait
//
// sessionID: 会话 ID
func (c *Client) waitSlot(sessionID string) {
	deadline := time.Now().Add(supersedeWait)
	for time.Now().Before(deadline) {
		if _, running := c.reg.TurnID(sessionID); !running {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// CancelTurn 取消某会话的在途轮次
//
// 幂等：无在途轮次时直接返回 false，不报错
// sessionID: 会话 ID
// return: 确实取消了在途轮次返回 true
func (c *Client) CancelTurn(sessionID string) bool {
	return c.reg.Cancel(sessionID)
}

// TurnID 查询某会话在途轮次的 ID
//
// sessionID: 会话 ID
// return: 在途轮次 ID 与是否存在，供调用方判断是否需要先取消
func (c *Client) TurnID(sessionID string) (string, bool) {
	return c.reg.TurnID(sessionID)
}

// CloseSession 通知 Agent 释放会话
//
// 先取消在途轮次再发释放请求；释放失败只记录不阻塞调用方，房间回收不应被它拖住
// ctx: 上层上下文，用于整体取消
// sessionID: 会话 ID
// reason: 释放原因，取值 room_closed、client_left、agent_shutdown
// return: 释放请求失败时返回错误
func (c *Client) CloseSession(ctx context.Context, sessionID, reason string) error {
	c.reg.Cancel(sessionID)
	rpcCtx, cancel := context.WithTimeout(ctx, closeSessionTimeout)
	defer cancel()
	resp, err := c.pool.Stub(sessionID).CloseSession(rpcCtx, &llmpb.CloseSessionRequest{
		SessionId: sessionID,
		Reason:    reason,
	})
	if err != nil {
		logger.Warnw("通知 Agent 释放会话失败", "sessionID", sessionID, "reason", reason, "error", err)
		return err
	}
	logger.Infow("已通知 Agent 释放会话", "sessionID", sessionID, "reason", reason, "closed", resp.Closed)
	return nil
}

// Inflight 返回当前在途轮次数
//
// return: 在途轮次数量
func (c *Client) Inflight() int {
	return c.reg.Inflight()
}

// Stats 读取累计计数
//
// return: 计数快照
func (c *Client) Stats() Stats {
	return Stats{
		Sent:      atomic.LoadInt64(&c.sent),
		Responses: atomic.LoadInt64(&c.responses),
		Cancelled: atomic.LoadInt64(&c.cancelled),
		Preempted: atomic.LoadInt64(&c.preempted),
		Rejected:  atomic.LoadInt64(&c.rejected),
		TimedOut:  atomic.LoadInt64(&c.timedOut),
		Failed:    atomic.LoadInt64(&c.failed),
		Retried:   atomic.LoadInt64(&c.retried),
	}
}

// Close 关闭连接池
//
// return: 关闭错误，全部成功时返回 nil
func (c *Client) Close() error {
	return c.pool.Close()
}

// runTurn 发起 RPC 并消费响应流，结束时清理在途登记
//
// ctx: 该轮 context，超时由 SendTurn 设置
// cancel: 该轮 context 的取消函数
// req: 轮次请求
func (c *Client) runTurn(ctx context.Context, cancel context.CancelFunc, req *llmpb.TurnRequest) {
	defer cancel()
	defer c.reg.End(req.SessionId, req.TurnId)
	defer func() {
		// 分发回调由调用方提供，兜住 panic 以免拖垮进程
		if r := recover(); r != nil {
			logger.Warnw("响应分发 panic", "sessionID", req.SessionId, "turnID", req.TurnId, "panic", r)
		}
	}()

	received, err := c.streamOnce(ctx, c.pool.Stub(req.SessionId), req)
	if err != nil && !received && retryableConnErr(err) && c.pool.Shards() > 1 {
		// 首选分片在收到任何响应前就连接级失败：改投相邻分片重试一次
		atomic.AddInt64(&c.retried, 1)
		observability.AgentRetried.Add(ctx, 1)
		logger.WarnwCtx(ctx, "分片不可用，改投相邻分片", "sessionID", req.SessionId, "turnID", req.TurnId, "error", err)
		_, err = c.streamOnce(ctx, c.pool.Next(req.SessionId), req)
	}
	if err == nil {
		return
	}
	switch {
	case isCancelled(err):
		atomic.AddInt64(&c.cancelled, 1)
		observability.IncAgentCancelled()
		logger.InfowCtx(ctx, "轮次已取消", "sessionID", req.SessionId, "turnID", req.TurnId)
		// 打断路径已在 sfu 侧 EndTurn，这里作为兜底（幂等）
		observability.EndTurn(req.TurnId, "interrupted")
	case status.Code(err) == codes.DeadlineExceeded:
		atomic.AddInt64(&c.timedOut, 1)
		observability.IncAgentTimedOut()
		logger.WarnwCtx(ctx, "轮次超时", "sessionID", req.SessionId, "turnID", req.TurnId, "timeout", c.turnTimeout)
		observability.EndTurn(req.TurnId, "error")
	default:
		atomic.AddInt64(&c.failed, 1)
		observability.IncAgentFailed()
		logger.WarnwCtx(ctx, "轮次异常结束", "sessionID", req.SessionId, "turnID", req.TurnId, "error", err)
		observability.EndTurn(req.TurnId, "error")
	}
}

// streamOnce 发起一次 Generate 并把响应逐条交给回调
//
// stub: 本次使用的分片客户端
// ctx: 该轮 context
// req: 轮次请求
// return: 是否已收到过响应，以及结束原因（正常结束为 nil）
func (c *Client) streamOnce(ctx context.Context, stub llmpb.LLMServiceClient, req *llmpb.TurnRequest) (bool, error) {
	stream, err := stub.Generate(ctx, req)
	if err != nil {
		return false, err
	}
	received := false
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return received, nil
		}
		if err != nil {
			return received, err
		}
		received = true
		atomic.AddInt64(&c.responses, 1)
		c.dispatch(ctx, resp)
	}
}

// retryableConnErr 判断错误是否属于「连接级不可用」
//
// 只有这类错误才值得改投其他分片；业务错误（如资源耗尽）重试无意义
// err: RPC 返回的错误
// return: 属于连接级不可用时返回 true
func retryableConnErr(err error) bool {
	return status.Code(err) == codes.Unavailable
}

// isCancelled 判断错误是否来自主动取消
//
// err: RPC 返回的错误
// return: 取消类错误返回 true
func isCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}
