// Package bwe 下行拥塞控制：发送侧带宽估计与有界整形
//
// 分成两半：估计器复用 pion 的 GCC 实现（delay-based 与 loss-based 取小），
// 整形器自己实现——pion 自带的 LeakyBucketPacer 队列无界且每包打日志，
// 在语音场景会把拥塞转换成无界延迟，这比丢包更糟
package bwe

import (
	"container/list"
	"sync"
	"time"

	"echat-backend/observability"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// dropReason 丢弃原因枚举
type dropReason string

const (
	// dropQueueFull 队列达到包数或字节上限
	dropQueueFull dropReason = "queue_full"
	// dropNoRoute 出队时找不到 SSRC 对应的写入器
	dropNoRoute dropReason = "no_route"
	// dropClosed 整形器已关闭后仍在入队
	dropClosed dropReason = "closed"
)

// Config 整形器参数
type Config struct {
	// MaxPackets 队列包数上限，超过即丢队首
	MaxPackets int
	// MaxBytes 队列字节上限，防止小包多发的场景绕过包数限制
	MaxBytes int
	// BurstFactor 令牌桶容量相对单个 20ms 帧字节数的倍数，控制瞬时突发
	BurstFactor float64
	// Tick 出队节拍，取 20ms 帧长的四分之一
	Tick time.Duration
	// InitialBitrate 初始目标速率，单位 bit/s
	InitialBitrate int
	// MaxBitrate 目标速率上界，用来算令牌桶容量，避免异常反馈把容量推到不可控
	MaxBitrate int
	// OverheadPermille RTP 层到线路层的开销补偿，180 表示放大 18%
	OverheadPermille int
}

// DefaultConfig 返回整形器的默认参数
//
// return: 可直接使用的配置
func DefaultConfig() Config {
	return Config{
		// 2 包 × 25ms（34kbps 下每包放行耗时）= 50ms 排队上限，
		// 与「pacer 排队延迟 p99 ≤ 60ms」的验收门槛自洽
		MaxPackets:     2,
		MaxBytes:       700,
		BurstFactor:    1.5,
		Tick:           5 * time.Millisecond,
		InitialBitrate: 64000,
		MaxBitrate:     128000,
	}
}

// packet 队列中的一个待发 RTP 包
type packet struct {
	// header RTP 头，入队时已克隆，调用方可复用原包
	header *rtp.Header
	// payload RTP 载荷，入队时已复制
	payload []byte
	// size 头加载荷的字节数，扣令牌与限流都按它算
	size int
	// attrs 拦截器属性，含 transport-cc 扩展 ID，出队时要原样透传
	attrs interceptor.Attributes
	// ssrc 该包所属流的标识，出队时按它路由写入器
	ssrc uint32
	// at 入队时刻，用于统计排队延迟
	at time.Time
}

// Pacer 每对端一份的有界令牌桶整形器
//
// 实现 gcc.Pacer 接口，可由 gcc.SendSideBWEPacer 注入给 SendSideBWE
// 同一 PeerConnection 的所有出流共享一份，这正是「一对端一份带宽预算」的语义
type Pacer struct {
	// cfg 整形参数，构造后只读
	cfg Config

	// mu 保护以下全部字段，出队循环会在写包时短暂释放
	mu sync.Mutex
	// streams SSRC 到内层写入器的路由表
	streams map[uint32]interceptor.RTPWriter
	// queue 待发包队列，队首最老
	queue *list.List
	// queuedBytes 当前队列占用字节数，与 queue 同步维护
	queuedBytes int
	// targetBitrate 当前整形速率，单位 bit/s
	targetBitrate int
	// tokens 当前可用令牌，单位为字节
	tokens float64
	// closed 是否已关闭
	closed bool

	// done 关闭信号
	done chan struct{}
	// closeOnce 保证 done 只关闭一次
	closeOnce sync.Once
}

// NewPacer 创建整形器并启动出队协程
//
// cfg: 整形参数，字段为零值时按 DefaultConfig 补齐
// return: 已启动的整形器实例
func NewPacer(cfg Config) *Pacer {
	def := DefaultConfig()
	if cfg.MaxPackets <= 0 {
		cfg.MaxPackets = def.MaxPackets
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = def.MaxBytes
	}
	if cfg.BurstFactor <= 0 {
		cfg.BurstFactor = def.BurstFactor
	}
	if cfg.Tick <= 0 {
		cfg.Tick = def.Tick
	}
	if cfg.InitialBitrate <= 0 {
		cfg.InitialBitrate = def.InitialBitrate
	}
	if cfg.MaxBitrate <= 0 {
		cfg.MaxBitrate = def.MaxBitrate
	}
	p := &Pacer{
		cfg:           cfg,
		streams:       make(map[uint32]interceptor.RTPWriter),
		queue:         list.New(),
		targetBitrate: cfg.InitialBitrate,
		done:          make(chan struct{}),
	}
	go p.loop()
	return p
}

// Write 入队一个出向包，永不阻塞
//
// header: RTP 头，内部会克隆，调用方后续可复用
// payload: RTP 载荷，内部会复制
// attributes: 拦截器属性，出队时原样透传
// return: 入队成功时返回包长，关闭或丢弃时返回 0
func (p *Pacer) Write(header *rtp.Header, payload []byte, attributes interceptor.Attributes) (int, error) {
	if header == nil {
		return 0, nil
	}
	hdr := header.Clone()
	buf := make([]byte, len(payload))
	copy(buf, payload)
	it := &packet{
		header:  &hdr,
		payload: buf,
		size:    hdr.MarshalSize() + len(payload),
		attrs:   attributes,
		ssrc:    header.SSRC,
		at:      time.Now(),
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.countDrop(dropClosed)
		return 0, nil
	}
	p.queue.PushBack(it)
	p.queuedBytes += it.size
	// 双上限：包数与字节数任一超限都丢队首，保证队列不会无界增长
	for p.queue.Len() > p.cfg.MaxPackets || p.queuedBytes > p.cfg.MaxBytes {
		front := p.queue.Front()
		if front == nil {
			break
		}
		old := front.Value.(*packet)
		p.queue.Remove(front)
		p.queuedBytes -= old.size
		p.countDropLocked(dropQueueFull)
	}
	p.mu.Unlock()
	return it.size, nil
}

// AddStream 注册一条流的写入器
//
// ssrc: 该流的同步源标识，出队时按它路由
// writer: 内层写入器，通常是 gcc 包装后的发送时刻记录器
func (p *Pacer) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	p.streams[ssrc] = writer
	p.mu.Unlock()
}

// RemoveStream 注销一条流，用于 AI 轨重建后清理旧 SSRC
//
// ssrc: 待移除的流标识
func (p *Pacer) RemoveStream(ssrc uint32) {
	p.mu.Lock()
	delete(p.streams, ssrc)
	p.mu.Unlock()
}

// SetTargetBitrate 更新整形速率，由估计器在目标变化时调用
//
// rate: 估计器给出的 RTP 层目标速率，单位 bit/s；非正数忽略
// 指标记录的是 RTP 层原值，便于与估计器日志对齐；实际放行速率会按开销补偿放大
func (p *Pacer) SetTargetBitrate(rate int) {
	if rate <= 0 {
		return
	}
	observability.RecordBweTarget(rate)
	effective := rate
	if p.cfg.OverheadPermille > 0 {
		effective = rate * (1000 + p.cfg.OverheadPermille) / 1000
	}
	p.mu.Lock()
	p.targetBitrate = effective
	p.mu.Unlock()
}

// TargetBitrate 返回当前整形速率
//
// return: 目标速率，单位 bit/s
func (p *Pacer) TargetBitrate() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.targetBitrate
}

// QueueDepth 返回当前队列包数
//
// return: 队列中的包数
func (p *Pacer) QueueDepth() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queue.Len()
}

// Close 停止出队协程并丢弃剩余包，幂等
//
// return: 固定返回 nil，满足 gcc.Pacer 接口
func (p *Pacer) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		dropped := p.queue.Len()
		p.queue.Init()
		p.queuedBytes = 0
		p.mu.Unlock()
		if dropped > 0 {
			observability.PacerDroppedAdd(int64(dropped), string(dropClosed))
		}
		close(p.done)
	})
	return nil
}

// loop 出队协程：按令牌桶节拍放行，直到关闭
func (p *Pacer) loop() {
	ticker := time.NewTicker(p.cfg.Tick)
	defer ticker.Stop()

	last := time.Now()
	for {
		select {
		case <-p.done:
			return
		case now := <-ticker.C:
			p.mu.Lock()
			elapsed := now.Sub(last).Seconds()
			last = now
			if elapsed > 0 {
				p.tokens += elapsed * float64(p.targetBitrate) / 8
			}
			if limit := p.bucketBytesLocked(); p.tokens > limit {
				p.tokens = limit
			}
			if depth := p.queue.Len(); depth > 0 {
				observability.RecordPacerQueueDepth(int64(depth))
			}
			for p.queue.Len() > 0 {
				front := p.queue.Front()
				it := front.Value.(*packet)
				if p.tokens < float64(it.size) {
					break
				}
				p.queue.Remove(front)
				p.queuedBytes -= it.size
				p.tokens -= float64(it.size)
				writer, ok := p.streams[it.ssrc]
				if !ok {
					p.countDropLocked(dropNoRoute)
					continue
				}
				// 写包时不持锁，避免慢写入器阻塞入队方
				p.mu.Unlock()
				observability.RecordPacerDelay(time.Since(it.at))
				if _, err := writer.Write(it.header, it.payload, it.attrs); err != nil {
					observability.RecordPacerWriteError()
				}
				p.mu.Lock()
			}
			p.mu.Unlock()
		}
	}
}

// bucketBytesLocked 计算令牌桶容量，调用方须持锁
//
// return: 容量字节数，取自一个 20ms 帧的大小乘以突发系数，并有下限保证小码率也能推进
func (p *Pacer) bucketBytesLocked() float64 {
	frameBytes := float64(p.targetBitrate) / 8 * 0.02
	limit := frameBytes * p.cfg.BurstFactor
	if limit < 300 {
		limit = 300
	}
	return limit
}

// countDrop 记录一次丢弃，不要求持锁
//
// reason: 丢弃原因
func (p *Pacer) countDrop(reason dropReason) {
	observability.PacerDroppedAdd(1, string(reason))
}

// countDropLocked 记录一次丢弃，调用方须持锁
//
// reason: 丢弃原因
func (p *Pacer) countDropLocked(reason dropReason) {
	observability.PacerDroppedAdd(1, string(reason))
}
