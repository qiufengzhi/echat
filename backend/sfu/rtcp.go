// rtcp.go 读取 SFU 出流的 RTCP 反馈，抽取抖动、丢包率、RTT，并解析 TWCC 的逐包回执
//
// 浏览器对「它收到的流」（我们发的中继轨与 AI TTS 轨）发 RR，RR 按 SSRC 路由到发送侧，
// 因此要读 RTPSender.ReadRTCP 而不是接收侧；每个出流一条读取协程，PC 关闭时自动退出
//
// TWCC 是 transport 级反馈，MediaSSRC 指向某一个已注册的发送 SSRC，因此只会落到
// 其中一条出流的读取协程上，不会重复计入；若远端把 MediaSSRC 填成未注册值，
// pion 会走 undeclaredRTCPMediaProcessor 丢弃，此时 twcc_feedback_total 恒为 0 可据此告警
package sfu

import (
	"echat-backend/observability"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// readSenderRTCP 持续读取某条出流的 RTCP 包，抽取 RR 指标并解析 TWCC
//
// clientID: 订阅方标识，仅用于日志
// kind: 出流类型，relay 表示中继轨、ai 表示 AI TTS 轨，用于指标分维度
// clock: 该订阅方的 SR 发送时刻表，为 nil 时不计算 RTT
// sender: 该出流对应的 RTPSender，ReadRTCP 阻塞直到有包或连接关闭
func (r *SFURoom) readSenderRTCP(clientID string, kind string, clock *srClock, sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, pkt := range pkts {
			switch p := pkt.(type) {
			case *rtcp.ReceiverReport:
				for _, rep := range p.Reports {
					// Jitter 单位 1/65536 秒 → 微秒；FractionLost 0-255 → 百分比
					jitterUs := int64(rep.Jitter) * 1_000_000 / 65536
					lossPercent := int64(rep.FractionLost) * 100 / 256
					observability.RecordRTCP(kind, jitterUs, lossPercent)
					if clock != nil {
						if rtt, ok := clock.rttFrom(rep); ok {
							observability.RecordRTT(rtt.Milliseconds())
						}
					}
				}
			case *rtcp.TransportLayerCC:
				observeTWCC(p)
			}
		}
	}
}

// observeTWCC 解析单个 TWCC 反馈，统计连续未收到包的段长并分桶上报
//
// 连续段跨 chunk 累加：一个 chunk 结束时若仍处于丢失态，run 不清零，
// 这样跨 chunk 的长丢失不会被切成两段
//
// fb: 远端发来的 transport-cc 反馈
func observeTWCC(fb *rtcp.TransportLayerCC) {
	observability.RecordTwccFeedback()

	run := 0
	flush := func() {
		if run > 0 {
			observability.RecordBurstLoss(run)
			run = 0
		}
	}

	for _, chunk := range fb.PacketChunks {
		switch c := chunk.(type) {
		case *rtcp.RunLengthChunk:
			n := int(c.RunLength)
			missing := c.PacketStatusSymbol == rtcp.TypeTCCPacketNotReceived
			for i := 0; i < n; i++ {
				if missing {
					run++
					continue
				}
				flush()
			}
		case *rtcp.StatusVectorChunk:
			for _, sym := range c.SymbolList {
				if sym == rtcp.TypeTCCPacketNotReceived {
					run++
					continue
				}
				flush()
			}
		}
	}
	flush()
}
