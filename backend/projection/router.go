package projection

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Config Router 运行参数
type Config struct {
	// NatsURL NATS 服务器地址，如 "127.0.0.1:4222"
	NatsURL string
	// Stream 绑定的 JetStream 流名（与 outbox relay 创建的流一致）
	Stream string
	// Durable durable consumer 名：重启后从 ack 断点续读
	Durable string
	// Batch 单次 Fetch 拉取上限
	Batch int
}

// 重投退避参数：不带延迟的 Nak 会让 JetStream 立刻把消息重投回来，
// 一条持续失败的事件足以把消费循环打成热循环（实测约 3ms 一轮、每秒数百次）
// 指数退避到上限后按固定间隔继续重投，等下游恢复或事件源修复后自然收敛
const (
	nakBaseDelay = time.Second
	nakMaxDelay  = 30 * time.Second
)

// reconnectPollInterval 断连期间等待 NATS 恢复的轮询间隔
// 这段窗口不做 Fetch：nats.go 重连后自动重订阅，恢复即按 durable 位点续读
const reconnectPollInterval = 500 * time.Millisecond

// Router 订阅 JetStream 并按事件 type 分发到各投影器
// 单 durable consumer 顺序消费 room.>，逐条投递，全部投影器成功才 Ack
type Router struct {
	cfg Config
	nc  *nats.Conn
	js  nats.JetStreamContext
	// handlers 事件类型 -> 关注该类型的投影器列表
	handlers map[string][]Projector
	// mu 保护 handlers 注册期的并发读写
	mu sync.RWMutex
}

// NewRouter 构造事件投影 Router
// cfg 见 Config 字段注释；handlers 注册由调用方在 Run 前完成
func NewRouter(cfg Config) *Router {
	return &Router{cfg: cfg, handlers: make(map[string][]Projector)}
}

// Handle 注册投影器关注的事件类型（同一投影器可关注多个类型）
// p 投影器，types 关注的事件类型白名单
func (r *Router) Handle(p Projector, types ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range types {
		r.handlers[t] = append(r.handlers[t], p)
	}
}

// projectorsOf 取关注某事件类型的投影器列表（注册期后只读，返回副本）
func (r *Router) projectorsOf(eventType string) []Projector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ps, ok := r.handlers[eventType]
	if !ok {
		return nil
	}
	out := make([]Projector, len(ps))
	copy(out, ps)
	return out
}

// Run 连接 NATS 后用 durable consumer 订阅 room.> 循环消费并按 type 分发
// 连接或建订阅失败均指数退避重试；处理成功才 ack，失败按投递次数退避后重投
// ctx 用于中断消费；返回 nil 表示被 ctx 取消退出
func (r *Router) Run(ctx context.Context) error {
	if err := r.connectNATS(ctx); err != nil {
		return err
	}
	defer r.nc.Close()

	sub, err := r.subscribe(ctx)
	if err != nil {
		return err
	}
	batch := r.cfg.Batch
	if batch <= 0 {
		batch = 16
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if !r.connected() {
			// 断连期间不发起 Fetch：否则每秒一次超时 Fetch 只会堆无效忙循环与警告日志
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(reconnectPollInterval):
			}
			continue
		}
		msgs, err := sub.Fetch(batch, nats.MaxWait(time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			logger.Warnw("消费拉取失败", "error", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, m := range msgs {
			r.handleMsg(ctx, m)
		}
	}
}

// connected 判断 NATS 当前是否已连上；消费循环据此跳过断连窗口
func (r *Router) connected() bool {
	return r.nc != nil && r.nc.Status() == nats.CONNECTED
}

// subscribe 建立 durable pull consumer，失败指数退避重试直至 ctx 取消
// durable consumer 重启后从上次 ack 断点续读，投递策略默认 All（未 ack 历史也会被重放）
func (r *Router) subscribe(ctx context.Context) (*nats.Subscription, error) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		sub, err := r.js.PullSubscribe("room.>", r.cfg.Durable, nats.BindStream(r.cfg.Stream))
		if err == nil {
			logger.Infow("投影 durable consumer 就绪", "durable", r.cfg.Durable, "subject", "room.>")
			return sub, nil
		}
		if errors.Is(err, nats.ErrStreamNotFound) {
			// 流由 outbox relay 负责创建：其未就绪时（重启恢复/首启竞态）等待建成后重试
			logger.Warnw("stream 未就绪（等待 outbox relay 创建），durable consumer 暂缓建立", "attempt", attempt, "error", err)
		} else {
			logger.Warnw("建立 durable consumer 失败，等待重试", "attempt", attempt, "error", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 15*time.Second {
			backoff = 15 * time.Second
		}
	}
}

// connectNATS 建立 NATS 连接与 JetStream 上下文，失败指数退避重试
func (r *Router) connectNATS(ctx context.Context) error {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		nc, err := nats.Connect(r.cfg.NatsURL,
			nats.Name("echat-projection-router"),
			nats.MaxReconnects(-1),
			nats.ReconnectWait(time.Second),
			nats.Timeout(5*time.Second),
			// 连接生命周期可观测：断连/恢复/永久关闭必须落日志，否则停机期间投影静默停摆无人知晓
			nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
				logger.Warnw("NATS 断连，投影暂停消费等待重连", "durable", r.cfg.Durable, "error", err)
			}),
			nats.ReconnectHandler(func(nc *nats.Conn) {
				logger.Infow("NATS 已重连，投影从 durable 位点续读", "url", nc.ConnectedUrl(), "durable", r.cfg.Durable)
			}),
			nats.ClosedHandler(func(_ *nats.Conn) {
				logger.Warnw("NATS 连接已永久关闭，投影停止消费（需人工介入）", "durable", r.cfg.Durable)
			}),
		)
		if err == nil {
			if js, jerr := nc.JetStream(); jerr == nil {
				r.nc = nc
				r.js = js
				logger.Infow("NATS 已连接，投影开始工作", "url", r.cfg.NatsURL, "durable", r.cfg.Durable)
				return nil
			} else {
				nc.Close()
				err = jerr
			}
		}
		logger.Warnw("NATS 连接失败，投影等待重试", "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// handleMsg 解码一条消息并按事件类型投递给关注的投影器
// 解码失败视为毒消息直接 Ack；投影失败按投递次数退避后 Nak 重投
func (r *Router) handleMsg(ctx context.Context, m *nats.Msg) {
	var ev Event
	if err := json.Unmarshal(m.Data, &ev); err != nil {
		logger.Warnw("投影消息解码失败，丢弃毒消息", "subject", m.Subject, "error", err)
		_ = m.Ack()
		return
	}
	handlers := r.projectorsOf(ev.Type)
	if len(handlers) == 0 {
		_ = m.Ack() // 无人关注的事件类型直接确认，避免无限堆积
		return
	}
	var firstErr error
	for _, p := range handlers {
		if err := p.Handle(ctx, ev); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		_ = m.Ack()
		return
	}
	delivered := deliveredCount(m)
	delay := nakDelay(delivered)
	logger.Warnw("投影失败，退避后重投", "type", ev.Type, "eventID", ev.ID,
		"delivered", delivered, "delay", delay, "error", firstErr)
	_ = m.NakWithDelay(delay)
}

// nakDelay 计算该消息本次失败后的重投延迟：第 n 次投递失败后等 base*2^(n-1)，上限 nakMaxDelay
// delivered 为已投递次数（含本次，首次为 1）
func nakDelay(delivered uint64) time.Duration {
	delay := nakBaseDelay
	for i := uint64(2); i <= delivered; i++ {
		delay *= 2
		if delay >= nakMaxDelay {
			return nakMaxDelay
		}
	}
	return delay
}

// deliveredCount 取该消息的已投递次数（含本次）；元数据不可得时按首次投递处理
func deliveredCount(m *nats.Msg) uint64 {
	if md, err := m.Metadata(); err == nil && md.NumDelivered > 0 {
		return md.NumDelivered
	}
	return 1
}
