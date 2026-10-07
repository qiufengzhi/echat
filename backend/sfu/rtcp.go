// rtcp.go 读取 SFU 对端的 RTCP Receiver Report，抽出抖动与丢包率
//
// 浏览器对「它收到的流」发 RR，因此这是下行媒体质量的直接观测；
// 每条对端一条读取协程，PeerConnection 关闭时 ReadRTCP 返回错误而退出
package sfu

import (
	"echat-backend/observability"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// readRTCP 持续读取对端的 RTCP 包，抽取 Receiver Report 的抖动与丢包率
//
// clientID: 对端标识，仅用于日志
// receiver: 该对端的 RTP 接收器，ReadRTCP 阻塞直到有包或连接关闭
func (r *SFURoom) readRTCP(clientID string, receiver *webrtc.RTPReceiver) {
	for {
		pkts, _, err := receiver.ReadRTCP()
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
