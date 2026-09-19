package signaling

import (
	"encoding/json"

	"github.com/pion/webrtc/v4"
)

// 信令消息类型常量（上行 = 客户端 -> 服务端，下行 = 服务端 -> 客户端）
const (
	// ---------- 客户端 -> 服务端 ----------
	MsgTypeJoin     = "join"      // 用户请求加入房间，payload 通常是昵称字符串
	MsgTypeOffer    = "offer"     // （已弃用，由 SFU 替代）WebRTC SDP Offer，保留占位以防旧客户端
	MsgTypeAnswer   = "answer"    // （已弃用）WebRTC SDP Answer
	MsgTypeICE      = "ice"       // （已弃用）WebRTC ICE 候选地址
	MsgTypeLeave    = "leave"     // 用户主动离开房间，房主可携带 next_host_id 指定下一任房主
	MsgTypePing     = "ping"      // 心跳探测，服务端收到后回复 pong，避免 WebSocket 被空闲断开
	MsgTypeAiToggle = "ai_toggle" // 切换 AI 助手开关

	// 成员管理信令：客户端 -> 服务端
	MsgTypeRaiseHand  = "raise_hand"  // 听众举手请求上麦，无需 payload
	MsgTypeApproveMic = "approve_mic" // 房主/副主持批准举手上麦，payload 带 target_user_id
	MsgTypeRejectMic  = "reject_mic"  // 房主/副主持拒绝举手，payload 带 target_user_id
	MsgTypeKickMic    = "kick_mic"    // 房主/副主持请 speaker 下麦，payload 带 target_user_id
	MsgTypeMuteMic    = "mute_mic"    // 房主/副主持静音/解除 speaker，payload 带 target_user_id + muted

	// SFU 信令：客户端 -> 服务端
	// 客户端创建 SDP Offer 后通过 sfu_offer 发给 SFU 引擎
	MsgTypeSFUOffer = "sfu_offer" // 客户端发起的 SDP Offer
	// 客户端的 ICE Candidate 通过 sfu_ice 发送给 SFU 引擎
	MsgTypeSFUICE = "sfu_ice" // 客户端的 ICE Candidate

	// ---------- 服务端 -> 客户端 ----------
	MsgTypePong        = "pong"         // 心跳响应，回应客户端 ping
	MsgTypeWaiting     = "waiting"      // 当前房间只有自己，服务端返回 host_id 让前端先标记房主
	MsgTypeRoomReady   = "room_ready"   // 房间已有其他成员，返回成员快照、host_id，并允许开始信令协商
	MsgTypeUserJoined  = "user_joined"  // 新成员加入，广播给房间已有成员，并携带当前 host_id
	MsgTypeUserLeft    = "user_left"    // 成员离开，通知剩余成员，并携带可能更新后的 host_id
	MsgTypeHostChanged = "host_changed" // 房主发生变更，通知剩余成员刷新房主展示和交接权限

	// SFU 信令：服务端 -> 客户端
	// SFU 引擎收到客户端发起的 Offer 后创建 Answer，通过 sfu_answer 发送给客户端
	MsgTypeSFUAnswer = "sfu_answer" // SFU 引擎回复的 SDP Answer
	// SFU 引擎收集到的 ICE Candidate 通过 sfu_ice 发送给客户端
	MsgTypeSFUICEServer = "sfu_ice" // SFU 引擎的 ICE Candidate

	// SFU renegotiation（重新协商）：当 AddTrack 后触发，由 SFU 向订阅者客户端发送新 Offer
	MsgTypeRenegotiationOffer = "sfu_renegotiation_offer" // SFU 向客户端发送的新 Offer（服务端 -> 客户端）
	// 客户端收到 renegotiation Offer 后回复 Answer
	MsgTypeRenegotiationAnswer = "sfu_renegotiation_answer" // 客户端回复的 Answer（客户端 -> 服务端）

	MsgTypeAiStatus = "ai_status" // 服务端回复 AI 语音助手的当前开关状态
	MsgTypeError    = "error"     // 服务端错误消息，payload.message 可给前端转换成用户提示

	// 成员管理信令：服务端 -> 客户端
	MsgTypeHandRaised  = "hand_raised"  // 有人举手，广播给全房间，房主展示审批入口
	MsgTypeMicApproved = "mic_approved" // 上麦获批，广播给全房间，席位角色更新为 speaker
	MsgTypeMicRejected = "mic_rejected" // 上麦被拒，定向发给举手人
	MsgTypeMicKicked   = "mic_kicked"   // 被请下麦，广播给全房间，席位角色更新为 listener
	MsgTypeMuted       = "muted"        // 被静音/解除静音，广播给全房间，叠加状态变化

	// 会话吊销 / 封禁联动：服务端主动踢下线
	MsgTypeKicked = "kicked" // 连接被吊销/封禁踢下线，payload 带 reason，随后以 4001 关闭码断连
)

// Message 是前后端 WebSocket 共用的信令信封，具体 payload 结构由 Type 决定
type Message struct {
	// Type 消息类型，如 join / host_changed 等
	Type string `json:"type"`
	// RoomID 消息所属房间 ID
	RoomID string `json:"roomId"`
	// UserID 服务端填充的发送者 ID，前端上行通常不需要传
	UserID string `json:"userId"`
	// Payload 原始 JSON 载荷，由具体消息处理函数按 Type 解析
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RoomUser 是返回给前端的成员摘要，只包含 UI 展示和身份判断必需字段
type RoomUser struct {
	// ID 成员 ID（鉴权后的用户 id），前端席位/成员列表主键
	ID string `json:"id"`
	// Username 成员昵称，用于席位和成员列表展示
	Username string `json:"username"`
	// Role 成员当前角色：host/cohost/speaker/listener，前端据此打角色标记
	Role string `json:"role"`
}

// WaitingPayload 在房间只有一个成员时发送，让首位用户立即看到自己是房主
type WaitingPayload struct {
	// HostID 当前房主 ID；第一位成员加入时通常就是自己的 ID
	HostID string `json:"hostId"`
}

// RoomReadyPayload 在房间可开始协商时发送给新加入者，提供完整房间快照
type RoomReadyPayload struct {
	// Users 当前房间成员列表，按加入时间稳定排序
	Users []RoomUser `json:"users"`
	// HostID 当前房主 ID，前端据此给席位打房主标记
	HostID string `json:"hostId"`
	// CanStart 是否可以开始 WebRTC Offer/Answer/ICE 协商
	CanStart bool `json:"canStart"`
}

// UserJoinedPayload 广播给房间已有成员，通知新成员加入并同步当前房主
type UserJoinedPayload struct {
	// UserID 新加入成员的用户 ID（鉴权后身份）
	UserID string `json:"userId"`
	// Username 新加入成员昵称
	Username string `json:"username"`
	// HostID 当前房主 ID，避免前端房主状态滞后
	HostID string `json:"hostId"`
}

// UserLeftPayload 广播给剩余成员，表示某位成员已离开
type UserLeftPayload struct {
	// UserID 离开成员的用户 ID（鉴权后身份）
	UserID string `json:"userId"`
	// HostID 离开后仍存在的房主 ID；房间清空时省略
	HostID string `json:"hostId,omitempty"`
}

// LeavePayload 是客户端主动离开时可携带的载荷，房主可用它指定下一任房主
type LeavePayload struct {
	// NextHostID 期望交接给的用户 ID；为空或无效时服务端自动选择
	NextHostID string `json:"nextHostId,omitempty"`
}

// ---------- SFU 信令载荷类型 ----------

// SFUOfferPayload 是客户端发起的 SDP Offer，发给 SFU 引擎用于创建 Answer
type SFUOfferPayload struct {
	// SDP Offer 字符串
	SDP string `json:"sdp"`
}

// SFUAnswerPayload 是 SFU 引擎回复的 SDP Answer，发给客户端完成协商
type SFUAnswerPayload struct {
	// SDP Answer 字符串
	SDP string `json:"sdp"`
}

// RenegotiationOfferPayload 是 SFU 向订阅者客户端发送的 renegotiation Offer
type RenegotiationOfferPayload struct {
	// SDP renegotiation SDP Offer
	SDP string `json:"sdp"`
}

// RenegotiationAnswerPayload 是客户端对 renegotiation Offer 的 Answer
type RenegotiationAnswerPayload struct {
	// SDP renegotiation SDP Answer
	SDP string `json:"sdp"`
}

// SFUICEPayload 是 SFU 与客户端之间交换的 ICE Candidate
type SFUICEPayload struct {
	// Candidate ICE 候选描述（SDP 中的候选行）
	Candidate string `json:"candidate"`
	// SDPMid 该候选所属的媒体轨道标识
	SDPMid string `json:"sdpMid"`
	// SDPMLineIndex 该候选在 SDP 媒体描述中的索引位置
	SDPMLineIndex *uint16 `json:"sdpMLineIndex"`
	// UsernameFragment ICE 用户名片段，用于跨域场景
	UsernameFragment string `json:"usernameFragment"`
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
	p := SFUICEPayload{Candidate: candidate.Candidate}
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

// AiToggleReq AI 助手开关请求载荷
type AiToggleReq struct {
	// Enable 是否启用 AI 助手
	Enable bool `json:"enable"`
}

// AiToggleRes 是服务端回复客户端当前 AI 语音助手状态的载荷
type AiToggleRes struct {
	// State 当前 AI 语音助手状态："offline" | "standby" | "online"
	State string `json:"state"`
}

// TargetUserPayload 管理类信令的通用目标载荷
type TargetUserPayload struct {
	// TargetUserID 被管理成员的用户 ID
	TargetUserID string `json:"targetUserId"`
	// Muted 是否为静音操作：true 静音 / false 解除静音
	Muted bool `json:"muted,omitempty"`
}

// HandRaisedPayload 举手广播载荷
type HandRaisedPayload struct {
	// UserID 举手成员的用户 ID
	UserID string `json:"userId"`
	// Username 举手成员昵称，前端审批入口可展示
	Username string `json:"username"`
}

// RoleChangedPayload 上麦/下麦后的角色更新广播载荷
type RoleChangedPayload struct {
	// UserID 角色发生变化的成员用户 ID
	UserID string `json:"userId"`
	// Username 成员昵称，用于席位展示
	Username string `json:"username"`
	// Role 变更后的角色：speaker / listener
	Role string `json:"role"`
}

// MicRejectedPayload 上麦被拒定向通知载荷
type MicRejectedPayload struct {
	// UserID 被拒成员的用户 ID
	UserID string `json:"userId"`
}

// MutedPayload 静音状态变化广播载荷
type MutedPayload struct {
	// UserID 被静音成员的用户 ID
	UserID string `json:"userId"`
	// Username 成员昵称，用于席位展示
	Username string `json:"username"`
	// Muted 静音状态：true 已静音 / false 已解除
	Muted bool `json:"muted"`
}
