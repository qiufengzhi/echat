// rtcp.go 读取 SFU 出流的 RTCP Receiver Report，抽出抖动与丢包率
//
// 浏览器对「它收到的流」（我们发的中继轨与 AI TTS 轨）发 RR，RR 按 SSRC 路由到发送侧，
// 因此要读 RTPSender.ReadRTCP 而不是接收侧；每个出流一条读取协程，PC 关闭时自动退出
package sfu

import (
	"echat-backend/observability"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// readSenderRTCP 持续读取某条出流的 RTCP 包，抽取 Receiver Report 的抖动与丢包率
//
// clientID: 订阅方标识，仅用于日志
// sender: 该出流对应的 RTPSender，ReadRTCP 阻塞直到有包或连接关闭
func (r *SFURoom) readSenderRTCP(clientID string, sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, pkt := range pkts {
			rr, ok := pkt.(*rtcp.ReceiverReport)
			if !ok {
				continue
			}
			for _, rep := range rr.Reports {
				// Jitter 单位 1/65536 秒 → 微秒；FractionLost 0-255 → 百分比
				jitterUs := int64(rep.Jitter) * 1_000_000 / 65536
				lossPercent := int64(rep.FractionLost) * 100 / 256
				observability.RecordRTCP(jitterUs, lossPercent)
			}
		}
	}
}
