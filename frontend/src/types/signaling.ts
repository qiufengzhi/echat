// SignalingMessageType 描述前端和信令服务器通过 WebSocket 交换的消息类型。
export type SignalingMessageType =
  | 'join' // 前端请求加入指定房间。
  | 'waiting' // 服务端通知当前房间还在等待另一位用户。
  | 'room_ready' // 服务端通知房间已有对端，可以开始创建 offer。
  | 'user_joined' // 服务端广播有新用户加入当前房间。
  | 'offer' // WebRTC SDP offer 信令，用于发起协商。
  | 'answer' // WebRTC SDP answer 信令，用于回应 offer。
  | 'ice' // WebRTC ICE candidate 信令，用于交换可连接地址。
  | 'user_left' // 服务端广播有用户离开当前房间。
  | 'error' // 服务端返回错误信息。
  | 'ping' // 前端发给服务端的心跳探测消息。
  | 'pong' // 服务端回复前端的心跳响应消息。

// SignalingRoomUser 是后端 room_ready 消息里返回的房间成员摘要。
export interface SignalingRoomUser {
  id: string // 后端生成的用户唯一标识。
  username: string // 用户进入房间时填写的显示名称。
}

// RoomReadyPayload 表示房间已有两人以上，可以开始 WebRTC 协商。
export interface RoomReadyPayload {
  users: SignalingRoomUser[] // 当前房间内的成员列表。
  can_start: boolean // 是否允许当前客户端开始创建 offer。
}

// UserJoinedPayload 表示有新用户进入当前房间。
export interface UserJoinedPayload {
  user_id: string // 新进入房间的用户 ID。
  username: string // 新进入房间的用户显示名称。
}

// UserLeftPayload 表示房间内某个用户已经离开。
export interface UserLeftPayload {
  user_id: string // 离开房间的用户 ID。
}

// SignalingErrorPayload 表示信令服务器返回的错误信息。
export interface SignalingErrorPayload {
  message?: string // 后端返回给用户看的错误说明。
}

// SignalingMessage 是信令服务器 WebSocket 的统一消息结构，payload 随 type 变化。
export interface SignalingMessage<TPayload = unknown> {
  type: SignalingMessageType // 消息类型，决定 payload 应该如何解析。
  room_id: string // 消息所属房间 ID。
  user_id?: string // 发送该消息的用户 ID，部分服务端消息可能为空。
  payload?: TPayload // 消息载荷，例如 SDP、ICE candidate 或房间成员信息。
}

// OutgoingSignalingMessage 是前端发给信令服务器的消息结构。
export interface OutgoingSignalingMessage<TPayload = unknown> {
  type: SignalingMessageType // 前端要发送的信令类型。
  room_id: string // 目标房间 ID。
  payload?: TPayload // 要转发给后端或对端浏览器的消息载荷。
}
