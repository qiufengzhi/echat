// ─── 前端信令消息类型 ───
// 这些类型与后端 room/const.go 中的常量一一对应
// 注意：SFU 架构下，前端主动发起 SDP Offer，服务端回复 Answer

// SignalingClientMessageType 描述前端主动发给信令服务器的消息类型
export type SignalingClientMessageType =
  | 'join'      // 前端请求加入指定房间
  | 'ping'      // 前端发给服务端的心跳探测消息
  | 'leave'     // 前端主动离开房间，可携带 next_host_id 指定下一任房主
  | 'sfu_offer' // SFU：前端发起 SDP Offer 给服务端
  | 'sfu_ice'    // SFU：前端转发 ICE Candidate 给服务端
  | 'sfu_renegotiation_answer' // SFU：前端回复 renegotiation Answer 给服务端
  | 'ai_toggle' // 前端（仅房主）请求切换 AI 语音助手的开关状态

// SignalingServerMessageType 描述服务端发给前端的房间状态和 SFU 信令消息类型
export type SignalingServerMessageType =
  | 'waiting'     // 服务端通知当前房间只有自己，并返回 host_id
  | 'room_ready'  // 服务端通知房间已有其他成员，可以开始 WebRTC 协商
  | 'user_joined' // 服务端广播有新用户加入当前房间
  | 'user_left'   // 服务端广播有用户离开当前房间
  | 'host_changed' // 服务端广播房主已变更
  | 'error'       // 服务端返回错误信息
  | 'pong'        // 服务端回复前端的心跳响应消息
  | 'sfu_answer'  // SFU：服务端回复 SDP Answer，前端设为远端描述完成协商
  | 'sfu_ice'     // SFU：服务端转发 ICE Candidate 给前端
  | 'sfu_renegotiation_offer' // SFU：服务端在 AddTrack 后发送的 renegotiation Offer
  | 'ai_status'   // 服务端回复发送者当前 AI 语音助手的开关状态

// SignalingMessageType 是前后端 WebSocket 共用的完整消息类型集合
export type SignalingMessageType = SignalingClientMessageType | SignalingServerMessageType

// SignalingRoomUser 是后端返回的房间成员摘要，供席位和成员列表展示
export interface SignalingRoomUser {
  id: string       // 后端生成的成员唯一标识
  username: string  // 用户进入房间时填写的显示名称
}

// WaitingPayload 表示当前用户已进入房间但还在等待其他成员
export interface WaitingPayload {
  host_id: string // 当前房主 ID；首位进入者通常就是房主
}

// RoomReadyPayload 表示房间已有两人以上，可以开始 WebRTC 协商
export interface RoomReadyPayload {
  users: SignalingRoomUser[] // 当前房间成员快照
  host_id: string             // 当前房主 ID，用于前端给席位打"房主"标记
  can_start: boolean          // 是否允许当前客户端开始创建 offer（前端在收到 room_ready 后创建 offer）
}

// UserJoinedPayload 表示有新用户进入当前房间
export interface UserJoinedPayload {
  user_id: string  // 新进入房间的用户 ID
  username: string  // 新进入房间的用户显示名称
  host_id: string   // 当前房主 ID，避免前端房主状态滞后
}

// UserLeftPayload 表示房间内某个用户已经离开
export interface UserLeftPayload {
  user_id: string    // 离开房间的用户 ID
  host_id?: string   // 离开后的房主 ID；房间清空或服务端未返回时为空
}

// HostChangedPayload 表示房主已经发生变化
export interface HostChangedPayload {
  host_id: string // 新房主的用户 ID
}

// LeavePayload 是前端主动离开时可传给服务端的载荷
export interface LeavePayload {
  next_host_id?: string // 房主离开时指定的下一任房主 ID；为空时服务端自动选择
}

// SignalingErrorPayload 表示信令服务器返回的错误信息
export interface SignalingErrorPayload {
  message?: string // 后端返回给用户看的错误说明
}

// ─── SFU 信令载荷类型 ───

// SFUOfferPayload 是前端发起的 SDP Offer，发给 SFU 引擎用于创建 Answer
export interface SFUOfferPayload {
  sdp: string   // SDP 描述文本
  type: 'offer' // 固定为 "offer"
}

// SFUAnswerPayload 是服务端回复的 SDP Answer，前端设为远端描述完成协商
export interface SFUAnswerPayload {
  sdp: string   // SDP 描述文本
  type: 'answer' // 固定为 "answer"
}

// SFUICEPayload 是前端与服务端之间交换的 ICE Candidate
// 字段与 RTCIceCandidateInit 兼容，可直接用于 `new RTCIceCandidate(payload)`
export interface SFUICEPayload {
  candidate: string        // ICE candidate 描述字符串
  sdpMLineIndex: number    // 该 candidate 对应的 media 行索引
  sdpMid: string           // 该 candidate 对应的 media 标识符
  usernameFragment: string // ICE 用户名片段（用于 ICE 一致性检查）
}

// RenegotiationOfferPayload 是服务端在 AddTrack 后发送的 renegotiation Offer
export interface RenegotiationOfferPayload {
  sdp: string   // renegotiation SDP Offer
}

// RenegotiationAnswerPayload 是前端回复 renegotiation Offer 的 Answer
export interface RenegotiationAnswerPayload {
  sdp: string   // renegotiation SDP Answer
}

// ─── AI 语音助手载荷 ───

// AITogglePayload 是前端请求切换 AI 语音助手开关的载荷
export interface AITogglePayload {
  enable: boolean // true 表示开启 AI 语音助手，false 表示关闭
}

// AIAssistantState 是 AI 语音助手的三态，与后端 global.AIState 对应
export type AIAssistantState = 'offline' | 'standby' | 'online'

// AIStatusPayload 是服务端回复 AI 语音助手当前状态的载荷
export interface AIStatusPayload {
  state: AIAssistantState // 当前 AI 语音助手状态："offline" | "standby" | "online"
}

// ─── 通用消息结构 ───

// SignalingMessage 是信令服务器 WebSocket 的统一消息结构，payload 随 type 变化
export interface SignalingMessage<TPayload = unknown> {
  type: SignalingMessageType // 消息类型，决定 payload 应该如何解析
  room_id: string             // 消息所属房间 ID
  user_id?: string            // 发送该消息的用户 ID，部分前端上行消息可以为空
  payload?: TPayload          // 消息载荷，例如 SDP、ICE candidate、成员信息或房主交接信息
}

// OutgoingSignalingMessage 是前端发给信令服务器的消息结构
export interface OutgoingSignalingMessage<TPayload = unknown> {
  type: SignalingClientMessageType // 前端要发送的信令类型
  room_id: string                    // 目标房间 ID
  payload?: TPayload                 // 要发送给后端或转发给对端浏览器的消息载荷
}
