// settings.ts 进入房间默认行为：localStorage 镜像（进房同步读）+ 服务端账号权威副本（跨端同步）
//
// 读路径：useVoiceRoom 进房时同步调 getRoomJoinDefaults()，只能走本地镜像；
// 写路径：设置页切换即 PUT 服务端并同步镜像，MainShell 挂载时从服务端拉取水合
import { authedFetch } from './auth'

const ROOM_DEFAULTS_KEY = 'echat:room-defaults'

// RoomJoinDefaults 进房时的默认行为，开关默认都关闭（静音进房）
export interface RoomJoinDefaults {
  micOnByDefault: boolean // 进房时是否默认开启麦克风，false=静音进房
  speakerOnByDefault: boolean // 进房时是否默认开启扬声器，false=静音播放远端声音
}

const DEFAULT_ROOM_JOIN_DEFAULTS: RoomJoinDefaults = {
  micOnByDefault: false,
  speakerOnByDefault: false,
}

// normalizeDefaults 逐项校验服务端/本地字段类型，非法值回退假值
function normalizeDefaults(data: Partial<RoomJoinDefaults> | null | undefined): RoomJoinDefaults {
  return {
    micOnByDefault: typeof data?.micOnByDefault === 'boolean' ? data.micOnByDefault : false,
    speakerOnByDefault:
      typeof data?.speakerOnByDefault === 'boolean' ? data.speakerOnByDefault : false,
  }
}

// readRoomJoinDefaults 读镜像；数据缺失或字段类型非法时逐项回退假值
function readRoomJoinDefaults(): RoomJoinDefaults {
  const raw = localStorage.getItem(ROOM_DEFAULTS_KEY)
  if (!raw) return DEFAULT_ROOM_JOIN_DEFAULTS
  return normalizeDefaults(JSON.parse(raw) as Partial<RoomJoinDefaults>)
}

// getRoomJoinDefaults 进房时同步读取本地镜像，供 useVoiceRoom 初始化麦克风/扬声器
export function getRoomJoinDefaults(): RoomJoinDefaults {
  try {
    return readRoomJoinDefaults()
  } catch {
    return DEFAULT_ROOM_JOIN_DEFAULTS
  }
}

// saveRoomJoinDefaults 写入本地镜像，失败静默
export function saveRoomJoinDefaults(defaults: RoomJoinDefaults): void {
  try {
    localStorage.setItem(ROOM_DEFAULTS_KEY, JSON.stringify(defaults))
  } catch {
    // 存储不可用时静默跳过
  }
}

// clearRoomJoinDefaultsCache 登出时清镜像，避免下一账号在重新水合前读到残留设置
export function clearRoomJoinDefaultsCache(): void {
  try {
    localStorage.removeItem(ROOM_DEFAULTS_KEY)
  } catch {
    // 存储不可用时静默跳过
  }
}

// 请求统一走 authedFetch：带 Bearer，遇 401 自动 refresh 换新 token 重试，缓解 access 15 分钟短命问题
// syncRoomJoinDefaultsFromServer 拉服务端账号权威副本并写入本地镜像，
// 供登录后、跨端同步后水合；请求或鉴权失败返回 false
export async function syncRoomJoinDefaultsFromServer(): Promise<boolean> {
  try {
    const res = await authedFetch('/api/v1/me/preferences')
    if (!res.ok) return false
    const data = (await res.json()) as Partial<RoomJoinDefaults> | null
    saveRoomJoinDefaults(normalizeDefaults(data))
    return true
  } catch {
    return false
  }
}

// persistRoomJoinDefaults 保存默认行为到服务端并同步本地镜像；失败返回 false
export async function persistRoomJoinDefaults(defaults: RoomJoinDefaults): Promise<boolean> {
  try {
    const res = await authedFetch('/api/v1/me/preferences', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(defaults),
    })
    if (!res.ok) return false
    saveRoomJoinDefaults(defaults)
    return true
  } catch {
    return false
  }
}