package sfu

import (
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// aiCallReq AI 语音助手req
type aiCallReq struct {
	sessionID string      // 会话ID
	roomId    string      // 房间ID
	clientId  string      // 客户端ID
	rtpPacket *rtp.Packet // RTP 包
	//forwardClient map[string]*webrtc.TrackLocalStaticRTP // 转发客户端
	clients map[string]*webrtc.TrackLocalStaticRTP // 房间内客户端列表
}
