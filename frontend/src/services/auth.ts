// auth.ts 轻量认证助手：登录/刷新/登出 + access 令牌本地存储
//
// 设计：access 放 localStorage 供 WebSocket ?token= 握手使用，refresh 走 httpOnly cookie
// 后端已签发，前端不接触 refresh 明文。voice 房间只读 access，失效时调用 refresh 换新后重连
const ACCESS_KEY = 'echat_access'
const USER_KEY = 'echat_user'

// AuthUser 后端 /auth/login 响应中的用户概要
export interface AuthUser {
  id: string // 用户全局稳定身份
  username: string // 用户名句柄
  display_name: string // 展示昵称
  avatar_url: string | null // 头像地址，可空
}

// getAccessToken 读取当前 access 令牌；未登录返回 null
export function getAccessToken(): string | null {
  return window.localStorage.getItem(ACCESS_KEY)
}

// getAuthUser 读取缓存的登录用户概要，用于首页显示"已登录"
export function getAuthUser(): AuthUser | null {
  const raw = window.localStorage.getItem(USER_KEY)
  if (!raw) return null
  try {
    return JSON.parse(raw) as AuthUser
  } catch {
    return null
  }
}

// isAuthed 是否已持有 access 令牌，HomePage 据此决定是否展示登录表单
export function isAuthed(): boolean {
  return Boolean(getAccessToken())
}

// login 用 用户名/邮箱 + 密码 换取双 token，access 存本地、refresh 由后端写 cookie
export async function login(identifier: string, password: string): Promise<AuthUser> {
  const res = await fetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ identifier, password }),
  })
  const data = await res.json()
  if (!res.ok) {
    throw new Error(data?.error?.message || '登录失败，请稍后重试')
  }
  saveSession(data.access_token, data.user)
  return data.user as AuthUser
}

// refresh 用 httpOnly cookie 里的 refresh 换新 access；失败说明会话已失效
export async function refresh(): Promise<boolean> {
  const res = await fetch('/api/v1/auth/refresh', {
    method: 'POST',
    credentials: 'include',
  })
  if (!res.ok) return false
  const data = await res.json()
  saveSession(data.access_token, data.user)
  return true
}

// saveSession 持久化 access 与用户概要，供 WS 握手与首页展示
function saveSession(accessToken: string, user?: AuthUser): void {
  window.localStorage.setItem(ACCESS_KEY, accessToken)
  if (user) {
    window.localStorage.setItem(USER_KEY, JSON.stringify(user))
  }
}

// logout 清除本地令牌并通知后端吊销当前会话（best-effort，不阻塞 UI）
export function logout(): void {
  window.localStorage.removeItem(ACCESS_KEY)
  window.localStorage.removeItem(USER_KEY)
  void fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'include' }).catch(() => {})
}