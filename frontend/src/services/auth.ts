// auth.ts 轻量认证助手：登录/刷新/登出 + access 令牌本地存储
//
// 设计：access 放 localStorage 供 WebSocket ?token= 握手使用，refresh 走 httpOnly cookie
// 后端已签发，前端不接触 refresh 明文。voice 房间只读 access，失效时调用 refresh 换新后重连
import { clearRoomJoinDefaultsCache } from './settings'

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

// authedFetch 带 Bearer 的 REST 请求：遇 401 先用 refresh 换新 access 重试一次
// access 仅 15 分钟短命，而 REST 调用分布在整个使用周期内，不能像信令重连那样只在连前刷一次；
// 这里对过期 token 自愈，refresh 失败或二次 401 才把结果原样交还调用方
export async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const send = (token: string | null) =>
    fetch(input, {
      ...init,
      headers: {
        ...init.headers,
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
    })

  const res = await send(getAccessToken())
  if (res.status !== 401) return res

  const renewed = await refresh()
  if (!renewed) return res
  return send(getAccessToken())
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
  clearRoomJoinDefaultsCache()
  void fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'include' }).catch(() => {})
}

// RegisterInput 是注册接口的请求体，username 为唯一句柄、password 满足强度、email 可选
export interface RegisterInput {
  username: string // 用户唯一句柄（3-20 位字母数字 _ -）
  password: string // 登录密码（后端要求至少 8 位）
  email?: string // 联系邮箱；不填则注册为本地账号，填了则进入邮箱验证流程
}

// RegisterResult 是注册接口的响应，注册成功不签发 token，前端需另调 login 完成自动登录
export interface RegisterResult {
  id: string // 新用户全局唯一 id
  username: string // 已注册的用户句柄
  email: string | null // 绑定的邮箱；未填邮箱为 null
  status: string // 账户状态：本地账号 active，邮箱路径 pending
  need_verify: boolean // 是否需邮箱验证（填了 email 为 true，此时不能自动登录）
}

// register 创建账号；带 email 走邮箱验证路径（返回 need_verify），否则本地账号直接可用
export async function register(input: RegisterInput): Promise<RegisterResult> {
  const body: Record<string, string> = { username: input.username, password: input.password }
  if (input.email) body.email = input.email
  const res = await fetch('/api/v1/auth/register', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(body),
  })
  const data = await res.json()
  if (!res.ok) {
    throw new Error(data?.error?.message || '注册失败，请稍后重试')
  }
  return data as RegisterResult
}