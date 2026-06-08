// 本文件集中描述声聊间页面层使用的类型，避免页面组件直接散落字符串和临时对象。

export type AppView =
  | 'home' // 首页/进入页，用户在这里创建或加入声聊间。
  | 'room' // 声聊间页，用户已经进入房间并开始连麦或等待朋友。
  | 'left' // 离开页，用户已经退出房间并释放麦克风。

export type RoomConnectionTone =
  | 'ready' // 当前连接可用，页面可以显示稳定状态。
  | 'waiting' // 已进入房间但还在等待其他成员。
  | 'reconnecting' // 连接短暂异常，页面应温和提示正在恢复。

export type RoomParticipantRole =
  | 'host' // 房主，通常是创建房间的人。
  | 'member' // 普通成员，已经进入当前声聊间。
  | 'empty' // 空席位，用于邀请更多朋友加入。

export interface VoiceRoomMember {
  id: string // 成员唯一标识，真实成员来自后端，空席位使用前端生成的占位 ID。
  name: string // 页面展示的成员昵称。
  role: RoomParticipantRole // 成员在席位中的角色，用于显示房主或空席位。
  isSelf: boolean // 是否为当前用户自己，用于标记“我”和同步本地静音状态。
  isMuted: boolean // 该成员是否静音。
  isSpeaking: boolean // 该成员是否正在说话，第一阶段主要用于 UI 表达和后续音量检测扩展。
  isOnline: boolean // 该成员是否在线，空席位和已离开成员为 false。
}

export interface RoomStatusCopy {
  connectionText: string // 顶部连接状态文案，例如“连接成功”或“正在重新连接”。
  qualityText: string // 声音体验状态文案，例如“声音流畅”或“等待朋友”。
  tone: RoomConnectionTone // 状态颜色和强调程度。
}

export interface RoomActivityItem {
  id: string // 动态唯一标识，用于 React 渲染列表。
  title: string // 动态主文案，例如“你已进入声聊间”。
  detail: string // 动态补充说明，解释当前状态或下一步。
}

export interface LeaveRoomSummary {
  roomId: string // 刚刚离开的房间号。
  username: string // 当前用户昵称，用于重新加入时复用。
  durationText: string // 本次停留时长的展示文案。
  memberCount: number // 离开前页面知道的成员数量。
}

export interface JoinRoomInput {
  roomId: string // 用户输入或页面生成的房间号。
  username: string // 用户填写的昵称。
}
