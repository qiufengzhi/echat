// rooms.ts 频道级查询：当前只有「加入前校验频道号存在」，其余列表/搜索接口后端未开放
import { getAccessToken } from './auth'

// roomExists 查询频道号当前是否有在线房间，用于「加入」前校验，避免把不存在的号误建为新频道
// 返回 null 表示查询本身失败（网络或鉴权），调用方可提示稍后重试，不与「不存在」混淆
export async function roomExists(roomCode: string): Promise<boolean | null> {
  const token = getAccessToken()
  const res = await fetch(`/api/v1/rooms/${encodeURIComponent(roomCode)}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  if (!res.ok) return null
  const data = await res.json()
  return Boolean(data?.exists)
}