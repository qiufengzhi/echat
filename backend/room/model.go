package room

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// ConnIdentity 握手鉴权后的连接身份，取代早期「每连接随机 UUID 当身份」
// 身份（UserID）来自 access token 的 sub，服务端权威，客户端无法伪造
type ConnIdentity struct {
	// UserID 鉴权后的用户 id（权威身份，来自 access 的 sub）
	UserID string
	// SessionID 签发 access 的会话 id，吊销时按此精确踢连接
	SessionID string
	// TokenVersion 签发时的 token_version，预留吊销级联比对
	TokenVersion int
}

// Client 表示一个已连接的浏览器客户端，以及它在房间中的成员信息
type Client struct {
	ConnID       string          // 连接级唯一 id（服务端生成），作为 SFU peer 与通道键
	UserID       string          // 鉴权后的用户 id（权威身份，所有上行身份以绑定值为准）
	SessionID    string          // 签发 access 的会话 id，吊销联动时精确踢连接
	TokenVersion int             // 签发时的 token_version，预留吊销级联比对
	RoomID       string          // 客户端当前所在房间 ID，尚未加入房间时为空
	Username     string          // 用户进入房间时填写的展示昵称
	JoinedAt     time.Time       // 加入房间时间，用于生成稳定的成员列表排序
	Conn         MessageFramer // 信令传输帧通道（WebSocket 连接或 WebTransport 双向流适配器）
	Send         chan []byte     // 单客户端发送队列，由 writePump 串行写入 WebSocket
	closeOnce    sync.Once       // 确保离开/断连清理只执行一次，避免重复关闭通道或连接
}

// Room 表示一个信令房间，后端在这里维护成员列表、权威房主与互斥角色
type Room struct {
	ID      string             // 房间对外短码，由前端创建或输入（对应持久层 rooms.room_code）
	AggID   string             // 房间内部聚合根 id（uuid 串），事件骨干与授权投影的编排坐标，不对外展示
	HostID  string             // 当前房主的用户 ID；房主离开时会重新选择
	Clients map[string]*Client // 当前在线成员，以连接 ID（ConnID）为键
	RoleOf  map[string]string  // 成员权威角色映射（userID -> host/cohost/speaker/listener），互斥，缺失即 listener
	MutedOf map[string]bool    // 被静音成员集合（叠加的负向状态，覆盖 speak 权限）
	WaitingOf map[string]bool  // 举手待上麦成员集合（userID -> 已举手），approve/reject 时收敛，leave 时清除
	Lock    sync.RWMutex       // 保护 HostID、Clients、RoleOf、MutedOf、WaitingOf 的并发读写
}

// RoomUser 是返回给前端的成员摘要，只包含 UI 展示和身份判断必需字段
type RoomUser struct {
	ID       string `json:"id"`       // 成员 ID（鉴权后的用户 id），前端席位/成员列表主键
	Username string `json:"username"` // 成员昵称，用于席位和成员列表展示
	Role     string `json:"role"`     // 成员当前角色：host/cohost/speaker/listener，前端据此打角色标记
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
	UserID   string `json:"user_id"`  // 新加入成员的用户 ID（鉴权后身份）
	Username string `json:"username"` // 新加入成员昵称
	HostID   string `json:"host_id"`  // 当前房主 ID，避免前端房主状态滞后
}

// UserLeftPayload 广播给剩余成员，表示某位成员已离开
type UserLeftPayload struct {
	UserID string `json:"user_id"`           // 离开成员的用户 ID（鉴权后身份）
	HostID string `json:"host_id,omitempty"` // 离开后仍存在的房主 ID；房间清空时省略
}

// LeavePayload 是客户端主动离开时可携带的载荷，房主可用它指定下一任房主
type LeavePayload struct {
	NextHostID string `json:"next_host_id,omitempty"` // 期望交接给的用户 ID；为空或无效时服务端自动选择
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

// TargetUserPayload 管理类信令的通用目标载荷
type TargetUserPayload struct {
	TargetUserID string `json:"target_user_id"` // 被管理成员的用户 ID
	Muted        bool   `json:"muted,omitempty"` // 是否为静音操作：true 静音 / false 解除静音
}

// HandRaisedPayload 举手广播载荷
type HandRaisedPayload struct {
	UserID   string `json:"user_id"`   // 举手成员的用户 ID
	Username string `json:"username"`  // 举手成员昵称，前端审批入口可展示
}

// RoleChangedPayload 上麦/下麦后的角色更新广播载荷
type RoleChangedPayload struct {
	UserID   string `json:"user_id"`  // 角色发生变化的成员用户 ID
	Username string `json:"username"` // 成员昵称，用于席位展示
	Role     string `json:"role"`     // 变更后的角色：speaker / listener
}

// MicRejectedPayload 上麦被拒定向通知载荷
type MicRejectedPayload struct {
	UserID string `json:"user_id"` // 被拒成员的用户 ID
}

// MutedPayload 静音状态变化广播载荷
type MutedPayload struct {
	UserID   string `json:"user_id"`  // 被静音成员的用户 ID
	Username string `json:"username"` // 成员昵称，用于席位展示
	Muted    bool   `json:"muted"`    // 静音状态：true 已静音 / false 已解除
}

// AiToggleRes 是服务端回复客户端当前 AI 语音助手状态的载荷
type AiToggleRes struct {
	State string `json:"state"` // 当前 AI 语音助手状态："offline" | "standby" | "online"
}

// Message 是前后端 WebSocket 共用的信令信封，具体 payload 结构由 Type 决定
type Message struct {
	Type    string          `json:"type"`              // 消息类型，如 join / offer / host_changed 等
	RoomID  string          `json:"room_id"`           // 消息所属房间 ID
	UserID  string          `json:"user_id"`           // 服务端填充的发送者 ID，前端上行通常不需要传
	Payload json.RawMessage `json:"payload,omitempty"` // 原始 JSON 载荷，由具体消息处理函数按 Type 解析
}
