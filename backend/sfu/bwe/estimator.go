package bwe

import (
	"fmt"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/rtcp"
)

// transportCCURI transport-wide-cc 头扩展的 URI，用于判断某条流是否协商了反馈
const transportCCURI = "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"

// Estimator 一个对端的带宽估计与整形组合
//
// 估计器按 PeerConnection 隔离：一条 UDP 链路共用一份带宽预算，
// 该链路下的所有出流（多条中继加一条 AI 轨）共享同一个 pacer
type Estimator struct {
	// BWE pion 的 GCC 估计器，delay-based 与 loss-based 各算一遍后取小
	BWE *gcc.SendSideBWE
	// Pacer 本对端共享的有界整形器
	Pacer *Pacer
}

// TargetBitrate 返回估计器当前的目标码率
//
// return: 目标码率，单位 bit/s
func (e *Estimator) TargetBitrate() int {
	if e == nil || e.BWE == nil {
		return 0
	}
	return e.BWE.GetTargetBitrate()
}

// Stats 返回估计器内部状态快照，供日志与健康状态机使用
//
// return: 键为损失分支码率、延迟分支码率、usage、state 等；未初始化时返回空 map
func (e *Estimator) Stats() map[string]any {
	if e == nil || e.BWE == nil {
		return map[string]any{}
	}
	return e.BWE.GetStats()
}

// QueueDepth 返回整形器当前队列深度
//
// return: 队列包数，未初始化时为 0
func (e *Estimator) QueueDepth() int {
	if e == nil || e.Pacer == nil {
		return 0
	}
	return e.Pacer.QueueDepth()
}

// Close 关闭估计器与整形器，幂等
//
// gcc 的 Close 会连带关闭注入的 pacer，而 pacer 的 Close 本身幂等，
// 因此这里再关一次是安全的
// return: 关闭过程中的错误，忽略整形器重复关闭
func (e *Estimator) Close() error {
	if e == nil {
		return nil
	}
	if e.BWE != nil {
		_ = e.BWE.Close()
	}
	if e.Pacer != nil {
		_ = e.Pacer.Close()
	}
	return nil
}

// Factory 为每个 PeerConnection 创建独立的估计器与整形器
//
// 拦截器链由 api.NewPeerConnection 同步构建，所以 onBuild 会在建连时同步触发，
// 调用方可以借此拿到句柄存到对端结构体上
type Factory struct {
	// baseOpts 透传给 gcc.NewSendSideBWE 的通用选项，含初始码率与上下界
	baseOpts []gcc.Option
	// pacerCfg 整形器参数
	pacerCfg Config
	// onBuild 估计器创建后的回调，可为 nil
	onBuild func(*Estimator)
}

// NewFactory 创建估计器工厂
//
// cfg: 整形器参数
// initial: 初始目标码率，必须高于稳态需求，否则一起步就开始丢包
// minRate: 估计下界
// maxRate: 估计上界
// onBuild: 估计器创建后的回调，可为 nil
// return: 可注册进 interceptor.Registry 的工厂
func NewFactory(cfg Config, initial, minRate, maxRate int, onBuild func(*Estimator)) *Factory {
	opts := []gcc.Option{}
	if initial > 0 {
		opts = append(opts, gcc.SendSideBWEInitialBitrate(initial))
	}
	if minRate > 0 {
		opts = append(opts, gcc.SendSideBWEMinBitrate(minRate))
	}
	if maxRate > 0 {
		opts = append(opts, gcc.SendSideBWEMaxBitrate(maxRate))
	}
	return &Factory{baseOpts: opts, pacerCfg: cfg, onBuild: onBuild}
}

// NewInterceptor 构建该 PeerConnection 专用的拦截器
//
// id: pion 传入的 PeerConnection 标识，本实现不使用
// return: 适配后的拦截器；估计器构造失败时返回错误
func (f *Factory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	p := NewPacer(f.pacerCfg)
	opts := make([]gcc.Option, 0, len(f.baseOpts)+1)
	opts = append(opts, f.baseOpts...)
	// 用自研有界整形器替换 pion 自带的无界漏桶
	opts = append(opts, gcc.SendSideBWEPacer(p))

	b, err := gcc.NewSendSideBWE(opts...)
	if err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("创建带宽估计器失败: %w", err)
	}
	est := &Estimator{BWE: b, Pacer: p}
	if f.onBuild != nil {
		f.onBuild(est)
	}
	return &Interceptor{est: est}, nil
}

// Interceptor 把估计器与整形器接进 pion 的拦截器链
//
// 必须注册在 RTCPReports 之后、transport-wide 序号拦截器之前：
// 序号要在包进入 pacer 队列前写好，估计器的发送时刻记录才有效
type Interceptor struct {
	interceptor.NoOp
	// est 本对端的估计器与整形器
	est *Estimator
}

// BindRTCPReader 让估计器看到远端反馈，同时把字节原样传给下游
//
// reader: 内层 RTCP 读取器，接口是字节级的，需要自己反序列化
// return: 包装后的读取器；解析失败或估计器内部出错都不影响下游，否则会连带损失 RR 观测
func (i *Interceptor) BindRTCPReader(reader interceptor.RTCPReader) interceptor.RTCPReader {
	return interceptor.RTCPReaderFunc(func(in []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attr, err := reader.Read(in, a)
		if err != nil || n == 0 {
			return n, attr, err
		}
		if pkts, uerr := rtcp.Unmarshal(in[:n]); uerr == nil && len(pkts) > 0 {
			_ = i.est.BWE.WriteRTCP(pkts, attr)
		}
		return n, attr, err
	})
}

// BindLocalStream 让本对端每条出流都经过共享整形器
//
// info: 流的协商信息，未协商 transport-wide 扩展时直接直通，避免无反馈也去限速
// writer: 内层写入器
// return: 整形器包装后的写入器
func (i *Interceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if !hasTransportCC(info) {
		return writer
	}
	return i.est.BWE.AddStream(info, writer)
}

// UnbindLocalStream 移除流在整形器中的路由，避免换轨后旧 SSRC 残留
//
// info: 被移除流的信息
func (i *Interceptor) UnbindLocalStream(info *interceptor.StreamInfo) {
	i.est.Pacer.RemoveStream(info.SSRC)
}

// Close 关闭估计器与整形器，避免每个对端泄漏一个整形协程
//
// return: 固定返回 nil
func (i *Interceptor) Close() error {
	return i.est.Close()
}

// hasTransportCC 判断该流是否协商了 transport-wide 头扩展
//
// info: 流的协商信息，可为 nil
// return: 协商了返回 true
func hasTransportCC(info *interceptor.StreamInfo) bool {
	if info == nil {
		return false
	}
	for _, ext := range info.RTPHeaderExtensions {
		if ext.URI == transportCCURI {
			return true
		}
	}
	return false
}
