// rtcpclock.go 出向 SR 的本地发送时刻表，用于从 RR 反算 RTT
//
// RFC 3550 的 RTT 算法需要 LSR 与 DLSR：LSR 是接收端回报「最后收到的 SR 时间戳」，
// DLSR 是接收端自己的驻留时长。只要发送端记录过发 SR 的本地时刻，
// 就能用 (now - t_sr) - DLSR 得到 RTT，整个过程不需要两端时钟同步
package sfu

import (
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
)

// srSent 一次 SR 发送记录
type srSent struct {
	// at 本地单调时钟下的发送时刻
	at time.Time
	// ntpMid SR 中 NTP 时间戳的中间 32 位，与 RR 的 LSR 字段比较以确认对齐
	ntpMid uint32
}

// srClock 记录每条出流最近一次 SR 的本地发送时刻
//
// 键是媒体 SSRC，RR 的每个 report block 通过 SSRC 对应回来
type srClock struct {
	// mu 保护 sentAt，读写分别在拦截器协程与 RTCP 读取协程
	mu sync.RWMutex
	// sentAt SSRC 到最近一次 SR 发送记录的映射
	sentAt map[uint32]srSent
}

// newSRClock 创建空的 SR 时刻表
//
// return: 可直接使用的时刻表
func newSRClock() *srClock {
	return &srClock{sentAt: make(map[uint32]srSent)}
}

// set 记录一次 SR 发送
//
// ssrc: 该 SR 对应的媒体 SSRC
// at: 本地发送时刻
// ntpMid: SR 中 NTP 时间戳的中间 32 位
func (c *srClock) set(ssrc uint32, at time.Time, ntpMid uint32) {
	c.mu.Lock()
	c.sentAt[ssrc] = srSent{at: at, ntpMid: ntpMid}
	c.mu.Unlock()
}

// lookup 查询某 SSRC 最近一次 SR 记录
//
// ssrc: 媒体 SSRC
// return: 发送记录与是否存在
func (c *srClock) lookup(ssrc uint32) (srSent, bool) {
	c.mu.RLock()
	rec, ok := c.sentAt[ssrc]
	c.mu.RUnlock()
	return rec, ok
}

// rttFrom 用 RR 的 report block 反算 RTT
//
// rep: RR 的 report block，含 LSR 与 DLSR
// return: RTT 与是否可用；LSR 对不上最近一次 SR 时视为过期，返回 false
func (c *srClock) rttFrom(rep rtcp.ReceptionReport) (time.Duration, bool) {
	if rep.LastSenderReport == 0 {
		return 0, false
	}
	rec, ok := c.lookup(rep.SSRC)
	if !ok || rec.ntpMid != rep.LastSenderReport {
		// LSR 指向更早的 SR 时，用当前记录会把 RTT 算大，直接丢弃这次样本
		return 0, false
	}
	// DLSR 单位是 1/65536 秒
	dlsr := time.Duration(rep.Delay) * time.Second / 65536
	rtt := time.Since(rec.at) - dlsr
	// 负值与超长值都视为异常样本，避免污染直方图
	if rtt <= 0 || rtt > 10*time.Second {
		return 0, false
	}
	return rtt, true
}

// srClockFactory 把 SR 发送时刻记录接进拦截器链
//
// 用 BindRTCPWriter 观察出向 RTCP：ConfigureRTCPReports 注册的 sender 拦截器周期发 SR，
// 本拦截器在它外层，先拿到 SR 记录时刻再放行
type srClockFactory struct {
	// clock 与 PeerConnection 共享的时刻表，其所有出流写入同一份
	clock *srClock
}

// newSRClockFactory 创建 SR 时刻记录拦截器的工厂
//
// clock: 与 SFUPeer 共享的时刻表实例
// return: 可注册进 interceptor.Registry 的工厂
func newSRClockFactory(clock *srClock) *srClockFactory {
	return &srClockFactory{clock: clock}
}

// NewInterceptor 构建该 PeerConnection 专用的 SR 记录拦截器
//
// id: pion 传入的 PeerConnection 标识，本实现不使用
// return: 拦截器实例，不会返回错误
func (f *srClockFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	return &srClockInterceptor{clock: f.clock}, nil
}

// srClockInterceptor 只在出向 RTCP 上工作，其余钩子沿用 NoOp
type srClockInterceptor struct {
	interceptor.NoOp
	// clock SR 发送时刻表
	clock *srClock
}

// BindRTCPWriter 记录出向 SR 的发送时刻后原样放行
//
// writer: 内层 RTCP 写入器
// return: 包装后的写入器，行为对调用方透明
func (i *srClockInterceptor) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	return interceptor.RTCPWriterFunc(func(pkts []rtcp.Packet, attributes interceptor.Attributes) (int, error) {
		now := time.Now()
		for _, pkt := range pkts {
			if sr, ok := pkt.(*rtcp.SenderReport); ok {
				i.clock.set(sr.SSRC, now, uint32(sr.NTPTime>>16))
			}
		}
		return writer.Write(pkts, attributes)
	})
}
