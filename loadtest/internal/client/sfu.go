// SFU 信令载荷（字段对齐 backend/room/signaling/wire.go 的 SFU 载荷类型）。
// 保持本包不依赖 pion/webrtc：场景层负责 webrtc 类型与载荷结构的转换。
package client

import (
	"encoding/json"
	"fmt"
)

// SFUOfferPayload 客户端 → SFU 的 SDP Offer（含 renegotiation offer 复用）
type SFUOfferPayload struct {
	SDP string `json:"sdp"`
}

// SFUAnswerPayload SFU → 客户端 的 SDP Answer（含 renegotiation answer 复用）
type SFUAnswerPayload struct {
	SDP string `json:"sdp"`
}

// SFUICEPayload ICE 候选（SDPMLineIndex 用指针以对齐 wire.go 的可空语义）
type SFUICEPayload struct {
	Candidate        string  `json:"candidate"`
	SDPMid           string  `json:"sdpMid"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex,omitempty"`
	UsernameFragment string  `json:"usernameFragment,omitempty"`
}

// SendSDP 发送 SDP 类信令（sfu_offer / sfu_answer / sfu_renegotiation_answer）
func (c *WSConn) SendSDP(msgType, roomID, sdp string) error {
	payload, err := json.Marshal(SFUOfferPayload{SDP: sdp})
	if err != nil {
		return err
	}
	return c.Send(Message{Type: msgType, RoomID: roomID, Payload: payload})
}

// SendICE 发送 ICE 候选
func (c *WSConn) SendICE(roomID string, p SFUICEPayload) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return c.Send(Message{Type: "sfu_ice", RoomID: roomID, Payload: payload})
}

// SendAIToggle 发送 AI 助手开关请求
func (c *WSConn) SendAIToggle(roomID string, enable bool) error {
	payload, err := json.Marshal(map[string]bool{"enable": enable})
	if err != nil {
		return err
	}
	return c.Send(Message{Type: "ai_toggle", RoomID: roomID, Payload: payload})
}

// ParseSFUPayload 解析信令 payload 为字符串（SDP 场景），解析失败返回错误
func ParseSFUPayload(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("payload 为空")
	}
	var p SFUAnswerPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}
	return p.SDP, nil
}

// ParseICEPayload 解析信令 payload 为 ICE 候选
func ParseICEPayload(raw json.RawMessage) (SFUICEPayload, error) {
	var p SFUICEPayload
	if len(raw) == 0 {
		return p, fmt.Errorf("payload 为空")
	}
	err := json.Unmarshal(raw, &p)
	return p, err
}
