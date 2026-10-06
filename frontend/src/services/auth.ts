// auth.ts 轻量认证助手：登录/刷新/登出 + access 令牌本地存储
//
// 设计：access 放 localStorage 供 WebSocket ?token= 握手使用，refresh 走 httpOnly cookie
// 后端已签发，前端不接触 refresh 明文。voice 房间只读 access，失效时调用 refresh 换新后重连
import { clearRoomJoinDefaultsCache } from './settings'
import { apiFetch, httpError, safeJson } from './request'

const ACCESS_KEY = 'echat_access'
const USER_KEY = 'echat_user'

// SessionResponse 登录/刷新成功响应体（与后端 SessionResult 对应），前端只消费这两字段
interface SessionResponse {
  accessToken: string // 短命无状态 JWT
  user: AuthUser // 登录用户概要
}

// AuthUser 后端 /auth/login 响应中的用户概要
export interface AuthUser {
  id: string // 用户全局稳定身份
  username: string // 用户名句柄
  displayName: string // 展示昵称
  avatarUrl: string | null // 头像地址，可空
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

// updateAuthUser 资料变更（昵称/头像）后刷新本地缓存的用户概要，其他页面即时读到新值
export function updateAuthUser(user: AuthUser): void {
  window.localStorage.setItem(USER_KEY, JSON.stringify(user))
}

// isAuthed 是否已持有 access 令牌，HomePage 据此决定是否展示登录表单
export function isAuthed(): boolean {
  return Boolean(getAccessToken())
}

// login 用 用户名/邮箱 + 密码 换取双 token，access 存本地、refresh 由后端写 cookie
// 失败时抛人话错误：业务 message 优先，网关 5xx/网络异常走 httpError 兜底，绝不外泄原始解析异常
export async function login(identifier: string, password: string): Promise<AuthUser> {
  const res = await apiFetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ identifier, password }),
  })
  const data = await safeJson<SessionResponse>(res)
  if (!res.ok) {
    throw httpError(res, data)
  }
  if (!data?.accessToken) {
    throw new Error('登录失败，请稍后重试')
  }
  saveSession(data.accessToken, data.user)
  return data.user as AuthUser
}

// RefreshOutcome 刷新结果的三种语义：renewed 换到新 access，unauthorized 会话已失效需重新登录，
// retry 服务侧或网络暂时不可用可稍后重试
// 区分 unauthorized 与 retry 是关键：前者再重试也不会成功（前端应停止重连并跳登录），后者值得继续重试
export type RefreshOutcome = 'renewed' | 'unauthorized' | 'retry'

// inflightRefresh 进行中的刷新请求，用于同页并发去重
// 轮换制刷新下，同一 cookie 并发调用会用到刚被轮换吊销的旧 refresh，被服务端判为重放而清算全部会话；
// 单飞让同一页面同一时刻只发一次请求
let inflightRefresh: Promise<RefreshOutcome> | null = null

// REFRESH_LOCK_NAME Web Locks 锁名：同名锁把刷新跨同源标签页串行化
const REFRESH_LOCK_NAME = 'echat:auth:refresh'
// REFRESH_LOCK_TIMEOUT_MS 等待刷新锁的上限：某标签页卡在刷新里时不让其他标签页的重连一直干等
const REFRESH_LOCK_TIMEOUT_MS = 10_000
// REFRESH_AT_KEY 跨标签页共享的最近一次成功刷新时间戳键（localStorage 同源共享）
const REFRESH_AT_KEY = 'echat_refresh_at'
// REFRESH_SKEW_MS 复用窗口：最近该时长内已成功刷新过，其他标签页直接复用共享的新 access，不再重复轮换
const REFRESH_SKEW_MS = 5_000

// refresh 用 httpOnly cookie 里的 refresh 换新 access
// 同页并发共享一个 in-flight 请求；跨标签页用 Web Locks 串行化，保证同一 cookie 同一时刻只有一次轮换
// 返回值：本次刷新的归类结果（renewed / unauthorized / retry），绝不抛原始异常污染调用方
export function refresh(): Promise<RefreshOutcome> {
  if (inflightRefresh) return inflightRefresh
  inflightRefresh = coordinatedRefresh().finally(() => {
    inflightRefresh = null
  })
  return inflightRefresh
}

// coordinatedRefresh 跨标签页协调刷新：抢到锁后再检查是否已被其他标签页刷新过
// 未抢到锁超时按 retry 处理，不阻塞上层重连
// 返回值：本次刷新的归类结果
async function coordinatedRefresh(): Promise<RefreshOutcome> {
  const locks = navigator.locks
  if (!locks) return doRefresh() // 环境无 Web Locks：退回仅同页单飞
  try {
    return await locks.request(
      REFRESH_LOCK_NAME,
      { signal: AbortSignal.timeout(REFRESH_LOCK_TIMEOUT_MS) },
      async () => {
        // 临界区内先看共享时间戳：刚被别的标签页刷新过就直接复用，跳过本次轮换
        if (recentlyRefreshed()) return getAccessToken() ? 'renewed' : 'retry'
        const outcome = await doRefresh()
        if (outcome === 'renewed') markRefreshed()
        return outcome
      },
    )
  } catch {
    return 'retry'
  }
}

// recentlyRefreshed 判断共享时间戳是否落在复用窗口内
// 返回值：true 表示最近已有标签页刷新成功，可直接复用共享的新 access
function recentlyRefreshed(): boolean {
  const at = Number(window.localStorage.getItem(REFRESH_AT_KEY))
  return at > 0 && Date.now() - at < REFRESH_SKEW_MS
}

// markRefreshed 记录本次刷新成功的时间戳，供其他标签页判断是否可复用
function markRefreshed(): void {
  window.localStorage.setItem(REFRESH_AT_KEY, String(Date.now()))
}

// doRefresh 真正执行一次刷新请求并归类结果
// 返回值：renewed 换到新 access 并已落本地，unauthorized 会话失效，retry 暂时不可用
async function doRefresh(): Promise<RefreshOutcome> {
  let res: Response
  try {
    res = await apiFetch('/api/v1/auth/refresh', {
      method: 'POST',
      credentials: 'include',
    })
  } catch {
    return 'retry' // 网络层失败（断网、后端不可达）：可能只是暂时，交给上层继续重试
  }
  // 400/401/403：refresh 已被吊销、无效或缺失，重试不会成功，判定为会话失效
  // 注意后端把「令牌无效」映射为 400 TOKEN_INVALID（而非 401），故 400 也归入终态
  if (res.status === 400 || res.status === 401 || res.status === 403) return 'unauthorized'
  if (!res.ok) return 'retry' // 5xx/429 等：服务侧问题，可重试
  const data = await safeJson<SessionResponse>(res)
  if (!data?.accessToken) return 'retry'
  saveSession(data.accessToken, data.user)
  return 'renewed'
}

// authedFetch 带 Bearer 的 REST 请求：遇 401 先用 refresh 换新 access 重试一次
// access 仅 15 分钟短命，而 REST 调用分布在整个使用周期内，不能像信令重连那样只在连前刷一次；
// 这里对过期 token 自愈，refresh 失败或二次 401 才把结果原样交还调用方
export async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const send = (token: string | null) =>
    apiFetch(input, {
      ...init,
      headers: {
        ...init.headers,
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
    })

  const res = await send(getAccessToken())
  if (res.status !== 401) return res

  const outcome = await refresh()
  if (outcome !== 'renewed') return res
  return send(getAccessToken())
}

// saveSession 持久化 access 与用户概要，供 WS 握手与首页展示
function saveSession(accessToken: string, user?: AuthUser): void {
  window.localStorage.setItem(ACCESS_KEY, accessToken)
  if (user) {
    window.localStorage.setItem(USER_KEY, JSON.stringify(user))
  }
}

// clearSession 仅清除本地令牌与用户概要缓存，不通知后端
// 用于会话已确认失效（refresh 返回 unauthorized）时，让路由守卫立刻把用户送回登录页
export function clearSession(): void {
  window.localStorage.removeItem(ACCESS_KEY)
  window.localStorage.removeItem(USER_KEY)
  window.localStorage.removeItem(REFRESH_AT_KEY)
  clearRoomJoinDefaultsCache()
}

// logout 清除本地令牌并通知后端吊销当前会话（best-effort，不阻塞 UI）
export function logout(): void {
  clearSession()
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
  needVerify: boolean // 是否需邮箱验证（填了 email 为 true，此时不能自动登录）
}

// register 创建账号；带 email 走邮箱验证路径（返回 needVerify），否则本地账号直接可用
// 失败时抛人话错误，与 login 相同的收敛策略
export async function register(input: RegisterInput): Promise<RegisterResult> {
  const body: Record<string, string> = { username: input.username, password: input.password }
  if (input.email) body.email = input.email
  const res = await apiFetch('/api/v1/auth/register', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(body),
  })
  const data = await safeJson<RegisterResult>(res)
  if (!res.ok) {
    throw httpError(res, data)
  }
  if (!data?.id) {
    throw new Error('注册失败，请稍后重试')
  }
  return data
}