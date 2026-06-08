export type SignalingMessageType =
  | 'join'
  | 'waiting'
  | 'room_ready'
  | 'user_joined'
  | 'offer'
  | 'answer'
  | 'ice'
  | 'user_left'
  | 'host_changed'
  | 'error'
  | 'ping'
  | 'pong'
  | 'leave'

export interface SignalingRoomUser {
  id: string
  username: string
}

export interface WaitingPayload {
  host_id: string
}

export interface RoomReadyPayload {
  users: SignalingRoomUser[]
  host_id: string
  can_start: boolean
}

export interface UserJoinedPayload {
  user_id: string
  username: string
  host_id: string
}

export interface UserLeftPayload {
  user_id: string
  host_id?: string
}

export interface HostChangedPayload {
  host_id: string
}

export interface LeavePayload {
  next_host_id?: string
}

export interface SignalingErrorPayload {
  message?: string
}

export interface SignalingMessage<TPayload = unknown> {
  type: SignalingMessageType
  room_id: string
  user_id?: string
  payload?: TPayload
}

export interface OutgoingSignalingMessage<TPayload = unknown> {
  type: SignalingMessageType
  room_id: string
  payload?: TPayload
}
