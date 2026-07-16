package room

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// Client 表示一个已连接的浏览器客户端，以及它在房间中的成员信息
type Client struct {
	ID        string          // 客户端唯一标识，由服务端在连接建立时生成
	RoomID    string          // 客户端当前所在房间 ID，尚未加入房间时为空
	Username  string          // 用户进入房间时填写的展示昵称
	JoinedAt  time.Time       // 加入房间时间，用于生成稳定的成员列表排序
	Conn      *websocket.Conn // 与浏览器保持的 WebSocket 连接实例
	Send      chan []byte     // 单客户端发送队列，由 writePump 串行写入 WebSocket
	closeOnce sync.Once       // 确保离开/断连清理只执行一次，避免重复关闭通道或连接
}

// Room 表示一个信令房间，后端在这里维护成员列表和权威房主
type Room struct {
	ID      string             // 房间唯一标识，由前端创建或输入
	HostID  string             // 当前房主的客户端 ID；房主离开时会重新选择
	Clients map[string]*Client // 当前在线成员，以 client.ID 为键
	Lock    sync.RWMutex       // 保护 HostID 和 Clients 的并发读写
}

// RoomUser 是返回给前端的成员摘要，只包含 UI 展示和身份判断必需字段
type RoomUser struct {
	ID       string `json:"id"`       // 成员 ID，对应 Client.ID
	Username string `json:"username"` // 成员昵称，用于席位和成员列表展示
}

// WaitingPayload 在房间只有一个成员时发送，让首位用户立即看到自己是房主
type WaitingPayload struct {
	HostID string `json:"host_id"` // 当前房主 ID；第一位成员加入时通常就是自己的 ID
}

// RoomReadyPayload 在房间可开始协商时发送给新加入者，提供完整房间快照
type RoomReadyPayload struct {
	Users    []RoomUser `json:"users"`     // 当前房间成员列表，按加入时间稳定排序
	HostID   string     `json:"host_id"`   // 当前房主 ID，前端据此给席位打房主标记
	CanStart bool       `json:"can_start"` // 是否可以开始 WebRTC Offer/Answer/ICE 协商
}

// UserJoinedPayload 广播给房间已有成员，通知新成员加入并同步当前房主
type UserJoinedPayload struct {
	UserID   string `json:"user_id"`  // 新加入成员 ID
	Username string `json:"username"` // 新加入成员昵称
	HostID   string `json:"host_id"`  // 当前房主 ID，避免前端房主状态滞后
}

// UserLeftPayload 广播给剩余成员，表示某位成员已离开
type UserLeftPayload struct {
	UserID string `json:"user_id"`           // 离开的成员 ID
	HostID string `json:"host_id,omitempty"` // 离开后仍存在的房主 ID；房间清空时省略
}

// LeavePayload 是客户端主动离开时可携带的载荷，房主可用它指定下一任房主
type LeavePayload struct {
	NextHostID string `json:"next_host_id,omitempty"` // 期望交接给的成员 ID；为空或无效时服务端自动选择
}

// ---------- SFU 信令载荷类型 ----------

// SFUOfferPayload 是客户端发起的 SDP Offer，发给 SFU 引擎用于创建 Answer
type SFUOfferPayload struct {
	SDP string `json:"sdp"` // SDP Offer 字符串
}

// SFUAnswerPayload 是 SFU 引擎回复的 SDP Answer，发给客户端完成协商
type SFUAnswerPayload struct {
	SDP string `json:"sdp"` // SDP Answer 字符串
}

// RenegotiationOfferPayload 是 SFU 向订阅者客户端发送的 renegotiation Offer
type RenegotiationOfferPayload struct {
	SDP string `json:"sdp"` // renegotiation SDP Offer
}

// RenegotiationAnswerPayload 是客户端对 renegotiation Offer 的 Answer
type RenegotiationAnswerPayload struct {
	SDP string `json:"sdp"` // renegotiation SDP Answer
}

// SFUICEPayload 是 SFU 与客户端之间交换的 ICE Candidate
// 服务端转发给客户端时携带 candidate 和 usernameFragment；客户端发给服务端时同理
type SFUICEPayload struct {
	Candidate        string  `json:"candidate"`        // ICE 候选描述（SDP 中的候选行）
	SDPMid           string  `json:"sdpMid"`           // 该候选所属的媒体轨道标识
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex"`    // 该候选在 SDP 媒体描述中的索引位置
	UsernameFragment string  `json:"usernameFragment"` // ICE 用户名片段，用于跨域场景
}

// ToWebRTCICECandidateInit 将 SFUICEPayload 转换为 pion/webrtc 的 ICECandidateInit
func (p *SFUICEPayload) ToWebRTCICECandidateInit() webrtc.ICECandidateInit {
	return webrtc.ICECandidateInit{
		Candidate:        p.Candidate,
		SDPMid:           &p.SDPMid,
		SDPMLineIndex:    p.SDPMLineIndex,
		UsernameFragment: &p.UsernameFragment,
	}
}

// SFUPayloadFromICECandidateInit 从 pion/webrtc 的 ICECandidateInit 转换为 SFU 信令载荷
func SFUPayloadFromICECandidateInit(candidate webrtc.ICECandidateInit) SFUICEPayload {
	p := SFUICEPayload{
		Candidate: candidate.Candidate,
	}
	if candidate.SDPMid != nil {
		p.SDPMid = *candidate.SDPMid
	}
	if candidate.SDPMLineIndex != nil {
		p.SDPMLineIndex = candidate.SDPMLineIndex
	}
	if candidate.UsernameFragment != nil {
		p.UsernameFragment = *candidate.UsernameFragment
	}
	return p
}

// AiToggleReq  AI 助手开关req
type AiToggleReq struct {
	Enable bool `json:"enable"` // 是否启用 AI 助手
}

// AiToggleRes 是服务端回复客户端当前 AI 语音助手状态的载荷
type AiToggleRes struct {
	Enable bool `json:"enable"` // 当前 AI 语音助手的实际开关状态
}

// Message 是前后端 WebSocket 共用的信令信封，具体 payload 结构由 Type 决定
type Message struct {
	Type    string          `json:"type"`              // 消息类型，如 join / offer / host_changed 等
	RoomID  string          `json:"room_id"`           // 消息所属房间 ID
	UserID  string          `json:"user_id"`           // 服务端填充的发送者 ID，前端上行通常不需要传
	Payload json.RawMessage `json:"payload,omitempty"` // 原始 JSON 载荷，由具体消息处理函数按 Type 解析
}
