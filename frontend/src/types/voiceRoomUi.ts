// 本文件集中描述声聊间页面层使用的类型，避免页面组件直接散落字符串和临时对象

import type { AIAssistantState } from './signaling'

export type RoomConnectionTone =
  | 'ready' // 当前连接可用，页面可以显示稳定状态
  | 'waiting' // 已进入房间但还在等待其他成员
  | 'reconnecting' // 连接短暂异常，页面应温和提示正在恢复

export type RoomParticipantRole =
  | 'host' // 房主，通常是创建房间的人，拥有全部管理权限
  | 'cohost' // 副主持，继承房主的管理权限（manage_ai / mod_mic）
  | 'speaker' // 上麦者，可发言，受静音叠加状态覆盖
  | 'listener' // 听众，默认角色，可听不可言，可通过举手申请上麦
  | 'ai' // AI 助手席位，系统固定的虚拟成员
  | 'empty' // 空席位，用于邀请更多朋友加入

export interface VoiceRoomMember {
  id: string // 成员唯一标识，真实成员来自后端，空席位使用前端生成的占位 ID
  name: string // 页面展示的成员昵称
  role: RoomParticipantRole // 成员在席位中的角色，用于显示房主或空席位
  isSelf: boolean // 是否为当前用户自己，用于标记"我"和同步本地静音状态
  isMuted: boolean // 该成员是否静音
  isSpeaking: boolean // 该成员是否正在说话，第一阶段主要用于 UI 表达和后续音量检测扩展
  isOnline: boolean // 该成员是否在线，空席位和已离开成员为 false
  hasAudio: boolean // SFU 下该成员是否有远端音频流到达
  isWaiting?: boolean // 该成员是否正在举手等待上麦，仅举手态成员为 true
  aiState?: AIAssistantState // AI 席位专用：当前 AI 三态，仅 role === 'ai' 时有值
}

export interface RoomStatusCopy {
  connectionText: string // 顶部连接状态文案，例如“连接成功”或“正在重新连接”
  qualityText: string // 声音体验状态文案，例如“声音流畅”或“等待朋友”
  tone: RoomConnectionTone // 状态颜色和强调程度
}
