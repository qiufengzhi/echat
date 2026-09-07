// 频道访问历史持久化在 localStorage，供「我的-历史记录」子页读取展示
const HISTORY_KEY = 'echat:room-history'
const HISTORY_LIMIT = 30 // 最多保留的条数，超出后丢弃最旧的记录

// RoomHistoryEntry 一条频道访问记录
export interface RoomHistoryEntry {
  roomId: string // 频道号
  joinedAt: number // 进入频道的时间戳，用于排序与展示
}

// recordRoomVisit 记录一次进入频道：同频道去重后置顶，超过上限时丢弃最旧记录
// 写入失败（隐私模式或存储满）时静默忽略，不影响进房主流程
export function recordRoomVisit(roomId: string): void {
  const entries = getRoomHistory().filter(entry => entry.roomId !== roomId)
  entries.unshift({ roomId, joinedAt: Date.now() })
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(entries.slice(0, HISTORY_LIMIT)))
  } catch {
    // 存储不可用时静默跳过
  }
}

// getRoomHistory 读取全部历史记录，数据损坏或缺失时按空列表处理
export function getRoomHistory(): RoomHistoryEntry[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw) as RoomHistoryEntry[]
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}