// rooms.ts 频道级查询：当前只有「加入前校验频道号存在」，其余列表/搜索接口后端未开放
import { authedFetch } from './auth'
import { safeJson } from './request'

// roomExists 查询频道号当前是否有在线房间，用于「加入」前校验，避免把不存在的号误建为新频道
// 返回三态：true=在线可加入；false=频道不存在（后端 404 ROOM_NOT_FOUND）或存在但离线（200 exists=false）；
// null=查询本身失败（网络、网关 5xx 或鉴权），调用方应提示稍后重试，不当作「不存在」处理
// 404 与查询失败必须区分，否则「频道不存在」这个明确答案会被误报成故障
export async function roomExists(roomCode: string): Promise<boolean | null> {
  let res: Response
  try {
    res = await authedFetch(`/api/v1/rooms/${encodeURIComponent(roomCode)}`)
  } catch {
    return null
  }
  if (res.status === 404) return false
  if (!res.ok) return null
  const data = await safeJson<{ exists: boolean }>(res)
  return Boolean(data?.exists)
}