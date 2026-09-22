// Package outbox 实现事务性 Outbox 的发布器（relayer）
//
// 职责：把随业务事务落库的 pending 事件投递到 NATS JetStream，并更新投递状态
// 对应设计：frontier-architecture-blueprint §3.2——「取→发→标」不是事务，
// 允许崩溃在任意一步，靠 Broker 层 Nats-Msg-Id 去重 + 下游消费幂等收敛
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"echat-backend/logging"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// 投递状态枚举，与 ent/schema/outboxevent.go 中 OutboxEventStatus* 一致
const (
	// StatusPending 待发送：随业务事务提交，等待 relay 投递
	StatusPending = "pending"
	// StatusSent 已发送：relay 已成功投递 JetStream 并落 published_at
	StatusSent = "sent"
	// StatusFailed 发送失败：attempts 达到上限，进入死信治理流程
	StatusFailed = "failed"
)

const (
	// publishMaxWait 单条事件同步发布的最大等待时长
	// nats.go 默认 5s：Broker 不可用时每条事件都要卡满超时，16 条一批能把 1s 的轮询周期拖成分钟级
	publishMaxWait = 2 * time.Second
	// outageLogInterval Broker 不可用日志的节流间隔：停机期间每轮都会触发，按固定间隔打一条避免刷屏
	outageLogInterval = 30 * time.Second
)

// Config relay 运行参数，由 config.Outbox 与 config.NATS 组合而来
type Config struct {
	// NatsURL NATS 服务器地址，如 "127.0.0.1:4222"
	NatsURL string
	// Stream JetStream 流名，事件按 subject 落入该流
	Stream string
	// StreamSubjects 流覆盖的 subject 前缀，如 user.> / room.>
	StreamSubjects []string
	// StreamMaxAge 事件保留时长，过期由 JetStream 自动回收
	StreamMaxAge time.Duration
	// PollInterval 轮询 pending 事件的间隔
	PollInterval time.Duration
	// BatchSize 单批取件上限
	BatchSize int
	// MaxAttempts 单条事件最大投递尝试次数，超限置 failed
	MaxAttempts int
}

// envelope 写入 JetStream 的事件信封，与蓝图 §3.2.4 对齐
type envelope struct {
	ID          uuid.UUID       `json:"id"`                // 唯一事件 id（= outbox 主键），消费者去重键
	Type        string          `json:"type"`              // 事件类型，如 user.registered
	AggregateID string          `json:"aggregate_id"`      // 所属聚合 id（此处为 user_id 字符串）
	OccurredAt  string          `json:"occurred_at"`       // 业务事务时间，RFC3339，非发布/消费时间
	Payload     json.RawMessage `json:"payload,omitempty"` // 事件载荷，原样透传 outbox.payload
}

// claimed 从 outbox 取出的待投递事件
type claimed struct {
	ID          uuid.UUID       // 事件主键，兼做 Broker 去重键
	Type        string          // 事件类型
	AggregateID uuid.UUID       // 聚合根 id
	Subject     string          // JetStream subject
	Payload     json.RawMessage // 事件载荷
	OccurredAt  time.Time       // 记账时间
}

// Relayer 事务性 Outbox 发布器：轮询 pending → 投递 JetStream → 更新状态
//
// NATS 不可用时事件留在 pending 堆积，连接恢复后自动补发——业务主链路不受影响
type Relayer struct {
	pool *pgxpool.Pool         // 数据库连接池，用于 SKIP LOCKED 取件与状态回写
	nc   *nats.Conn            // NATS 连接，客户端侧自带重连
	js   nats.JetStreamContext // JetStream 上下文，执行发布
	cfg  Config                // 运行参数
	log  *logging.Logger       // 模块日志
	// outageLoggedAt 上次打印「Broker 不可用」日志的 UnixNano，用于节流
	outageLoggedAt atomic.Int64
}

// NewRelayer 构造 relay 实例
// pool 数据库连接池，cfg 见 Config 字段注释
func NewRelayer(pool *pgxpool.Pool, cfg Config) *Relayer {
	return &Relayer{pool: pool, cfg: cfg, log: logging.New("outbox")}
}

// Run 启动 relay：先确保连上 NATS 并建流，再进入轮询投递循环
// ctx 建议传入服务生命周期上下文；退出时关闭 NATS 连接
func (r *Relayer) Run(ctx context.Context) {
	if err := r.connectNATS(ctx); err != nil {
		return
	}
	defer r.nc.Close()
	r.ensureStream(ctx) // 建流
	r.pollLoop(ctx)     // 轮询投递
}

// connectNATS 建立 NATS 连接，失败则指数退避重试（NATS 稍后可用时自动接上）
// ctx 用于中断重试；连接成功返回 nil 并把 JetStream 上下文存到 r.js
func (r *Relayer) connectNATS(ctx context.Context) error {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		nc, err := nats.Connect(r.cfg.NatsURL,
			nats.Name("echat-outbox-relay"),
			nats.MaxReconnects(-1), // 无限重连，断线重试交给客户端
			nats.ReconnectWait(time.Second),
			nats.Timeout(5*time.Second),
			// 连接生命周期可观测：断连/恢复/永久关闭必须落日志，否则停机期间只能靠投递失败反推
			nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
				r.log.Warnw("NATS 断连，事件堆积在 outbox 等待补发", "error", err)
			}),
			nats.ReconnectHandler(func(nc *nats.Conn) {
				r.log.Infow("NATS 已重连，开始补发堆积事件", "url", nc.ConnectedUrl())
			}),
			nats.ClosedHandler(func(_ *nats.Conn) {
				r.log.Warnw("NATS 连接已永久关闭，relay 停止投递", "url", r.cfg.NatsURL)
			}),
		)
		if err == nil {
			js, jerr := nc.JetStream()
			if jerr == nil {
				r.nc = nc
				r.js = js
				r.log.Infow("NATS 已连接，relay 开始工作", "url", r.cfg.NatsURL)
				return nil
			}
			nc.Close()
			err = jerr
		}
		r.log.Warnw("NATS 连接失败，事件将堆积在 outbox 等待补发", "attempt", attempt, "error", err)
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

// ensureStream 确保目标 JetStream 流存在；已存在则跳过，未建成则指数退避重试直至 ctx 取消
// 事件保留策略与去重窗口在此固化：event_id 去重允许 ~2 分钟内的重复发布被过滤
// 重试保证流必定建成：JetStream 从持久化卷恢复文件存储期 AddStream 可能瞬时失败，
// 而投影侧 durable consumer 绑定时流不存在会一直等，流创建失败绝不能静默放弃
func (r *Relayer) ensureStream(ctx context.Context) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		_, err := r.js.AddStream(&nats.StreamConfig{
			Name:       r.cfg.Stream,
			Subjects:   r.cfg.StreamSubjects,
			Storage:    nats.FileStorage, // 磁盘持久化，满足事件日志的持久与回放
			Retention:  nats.LimitsPolicy,
			MaxAge:     r.cfg.StreamMaxAge,
			Duplicates: 2 * time.Minute,
		})
		if err == nil {
			r.log.Infow("JetStream 流已创建", "stream", r.cfg.Stream, "subjects", r.cfg.StreamSubjects, "attempt", attempt)
			return
		}
		if errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return
		}
		r.log.Warnw("JetStream 流创建失败，等待重试", "stream", r.cfg.Stream, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 15*time.Second {
			backoff = 15 * time.Second
		}
	}
}

// pollLoop 按 PollInterval 轮询并投递 pending 事件
// 每轮之间可被 ctx 中断
func (r *Relayer) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.processBatch(ctx); err != nil {
			r.log.Warnw("处理 outbox 批次失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// processBatch 取一批 pending 事件并逐个投递，最后提交释放行锁
// 取件用 FOR UPDATE SKIP LOCKED，多实例并发取件不会重复（蓝图 §3.2.2③）
// 返回批次处理错误；单个事件投递失败只回写 attempts，不中断整批
// Broker 不可用时整轮跳过：既不占事务，也不让 publish 卡在超时上拖垮轮询周期
func (r *Relayer) processBatch(ctx context.Context) error {
	if r.brokerDown() {
		r.logOutageThrottled()
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch, err := r.claimBatch(ctx, tx)
	if err != nil {
		return err
	}
	for i := range batch {
		ev := &batch[i]
		if err = r.publish(ctx, ev); err != nil {
			if r.isBrokerFailure(err) {
				// 基础设施故障不计入 attempts：否则一次停机就会把整批事件按 MaxAttempts 打成 failed 死信，
				// 而 failed 没有回捞路径。这里保持 pending，等重连后下一轮自然补发
				r.logOutageThrottled()
				continue
			}
			r.log.Warnw("事件投递失败，等待下轮重试", "eventID", ev.ID, "subject", ev.Subject, "error", err)
			if err = r.markAttempt(ctx, tx, ev.ID); err != nil {
				r.log.Warnw("回写 attempts 失败", "eventID", ev.ID, "error", err)
			}
			continue
		}
		if err = r.markSent(ctx, tx, ev.ID); err != nil {
			r.log.Warnw("回写 sent 失败，Broker 去重托底", "eventID", ev.ID, "error", err)
		}
	}
	return tx.Commit(ctx)
}

// claimBatch 在事务内锁定并取出至多 cfg.BatchSize 条 pending 事件
// ctx 链路上下文，tx 当前事务；返回取出的待投递事件切片
func (r *Relayer) claimBatch(ctx context.Context, tx pgx.Tx) ([]claimed, error) {
	sql := `SELECT id, event_type, aggregate_id, subject, payload, created_at
FROM outbox_events
WHERE status = $1
ORDER BY created_at
LIMIT $2
FOR UPDATE SKIP LOCKED`
	rows, err := tx.Query(ctx, sql, StatusPending, r.cfg.BatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var batch []claimed
	for rows.Next() {
		var ev claimed
		var payload []byte
		if err := rows.Scan(&ev.ID, &ev.Type, &ev.AggregateID, &ev.Subject, &payload, &ev.OccurredAt); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(payload)
		batch = append(batch, ev)
	}
	return batch, rows.Err()
}

// publish 把事件封装成信封并发布到 JetStream，Nats-Msg-Id 用事件主键做 Broker 层去重
// ctx 链路上下文，ev 待投递事件；发布成功返回 nil
func (r *Relayer) publish(ctx context.Context, ev *claimed) error {
	env := envelope{
		ID:          ev.ID,
		Type:        ev.Type,
		AggregateID: ev.AggregateID.String(),
		OccurredAt:  ev.OccurredAt.UTC().Format(time.RFC3339Nano),
		Payload:     ev.Payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	msg := &nats.Msg{
		Subject: ev.Subject,
		Data:    data,
		Header:  nats.Header{"Nats-Msg-Id": []string{ev.ID.String()}},
	}
	// 同步 publish 的等待上限：Broker 不可用时不要卡满 nats.go 默认的 5s，
	// 否则一批 16 条会把整个轮询周期拖垮。Context 与 MaxWait 互斥，这里用带截止时间的 ctx
	pubCtx, cancel := context.WithTimeout(ctx, publishMaxWait)
	defer cancel()
	_, err = r.js.PublishMsg(msg, nats.Context(pubCtx))
	return err
}

// markSent 把事件标记为已发送：状态 sent + published_at + version+1
// ctx 链路上下文，tx 当前事务，id 事件主键
func (r *Relayer) markSent(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE outbox_events
SET status = $1, published_at = now(), attempts = attempts + 1, version = version + 1
WHERE id = $2`, StatusSent, id)
	return err
}

// markAttempt 记录一次失败尝试：attempts +1，达到上限则置 failed，否则保持 pending 等下轮重试
// 仅用于业务性失败；Broker 不可用的失败不得调用本函数，见 isBrokerFailure
// ctx 链路上下文，tx 当前事务，id 事件主键
func (r *Relayer) markAttempt(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE outbox_events
SET attempts = attempts + 1,
    status = CASE WHEN attempts + 1 >= $1 THEN $2 ELSE $3 END
WHERE id = $4`, r.cfg.MaxAttempts, StatusFailed, StatusPending, id)
	return err
}

// brokerDown 判断 NATS 当前是否不可用：断连、重连中或连接已关闭
// 以连接状态而非上一次错误作为判据，让轮询轮次与单条投递共用同一真相来源
func (r *Relayer) brokerDown() bool {
	return r.nc == nil || r.nc.Status() != nats.CONNECTED
}

// isBrokerFailure 区分「Broker 侧基础设施故障」与「业务性失败」
// 返回 true 表示失败源于连接/服务端不可用，事件应保持 pending 等待补发而不计入 attempts：
// attempts 预算的意义是给毒消息封顶，若停机也算进去，一次 outage 就会把整批健康事件打成 failed 死信
// 返回 false 表示按正常重试预算处理（markAttempt）
func (r *Relayer) isBrokerFailure(err error) bool {
	if r.brokerDown() {
		return true
	}
	return errors.Is(err, nats.ErrConnectionClosed) ||
		errors.Is(err, nats.ErrConnectionDraining) ||
		errors.Is(err, nats.ErrReconnectBufExceeded) ||
		errors.Is(err, nats.ErrNoStreamResponse)
}

// logOutageThrottled 节流打印 Broker 不可用日志
// 停机期间每轮（默认 1s）都会触发，按 outageLogInterval 打一条，避免日志洪水掩盖真实错误
func (r *Relayer) logOutageThrottled() {
	now := time.Now().UnixNano()
	last := r.outageLoggedAt.Load()
	if last != 0 && time.Duration(now-last) < outageLogInterval {
		return
	}
	if !r.outageLoggedAt.CompareAndSwap(last, now) {
		return
	}
	r.log.Warnw("NATS 不可用，本轮跳过投递；事件保持 pending 且不消耗 attempts，重连后自动补发",
		"status", r.statusText())
}

// statusText 取连接状态文本；nc 尚未建立时按断连处理
func (r *Relayer) statusText() string {
	if r.nc == nil {
		return nats.DISCONNECTED.String()
	}
	return r.nc.Status().String()
}
