// profile.ts 资料域服务：头像上传与资料编辑，响应统一解析 { user } 返回最新用户概要
import { authedFetch, type AuthUser } from './auth'
import { httpError, safeJson } from './request'

// parseUserResponse 从 { user: {...} } 成功响应体取用户概要；解析失败返回 null
function parseUserResponse(res: Response): Promise<AuthUser | null> {
  return res.json().then(
    data => (data && typeof data === 'object' && data.user ? (data.user as AuthUser) : null),
    () => null,
  )
}

// updateProfile PATCH /api/v1/me 更新昵称，返回最新用户概要供本地缓存同步
// 失败抛人话错误：后端业务 message 优先，网关 5xx/网络异常走 httpError 兜底
export async function updateProfile(patch: { displayName: string }): Promise<AuthUser> {
  const res = await authedFetch('/api/v1/me', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  })
  if (!res.ok) {
    throw httpError(res, await safeJson(res))
  }
  const user = await parseUserResponse(res)
  if (!user) {
    throw new Error('操作失败，请稍后重试')
  }
  return user
}

// uploadAvatar POST /api/v1/me/avatar 上传裁剪后的方形头像，成功后返回最新用户概要
// body 为 FormData，Content-Type 交由浏览器写入 multipart 边界，不要手动设置
// 失败抛人话错误，收敛策略与 updateProfile 一致
export async function uploadAvatar(blob: Blob): Promise<AuthUser> {
  const fd = new FormData()
  fd.append('file', blob, 'avatar')
  const res = await authedFetch('/api/v1/me/avatar', { method: 'POST', body: fd })
  if (!res.ok) {
    throw httpError(res, await safeJson(res))
  }
  const user = await parseUserResponse(res)
  if (!user) {
    throw new Error('操作失败，请稍后重试')
  }
  return user
}