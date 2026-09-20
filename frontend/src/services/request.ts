// request.ts REST 请求统一错误处理
//
// 目标：任何请求路径都不把原始错误文本（SyntaxError、Failed to fetch、nginx 网关的
// HTML 兜底页等）透传给页面，统一收敛成用户能看懂的人话文案
// 页面层只消费 err.message，只要 service 层 throw 出来的都是人话，页面就无需各自防御

// safeJson 尝试把响应体解析成 JSON；body 非 JSON 或解析失败返回 null，绝不向外抛解析异常
// res 待解析的响应；泛型 T 为预期的成功响应结构
export async function safeJson<T>(res: Response): Promise<T | null> {
  try {
    return (await res.json()) as T
  } catch {
    return null
  }
}

// apiFetch 发起一次请求，仅归一化网络层失败：fetch 直接抛错（后端域名不可达、断网）时
// 统一抛人话「网络异常」错误，HTTP 状态失败原样返回，交由调用方用 httpError 处理
// input 请求路径，init 与 fetch 同构的请求配置；返回原始 Response
export function apiFetch(input: string, init: RequestInit = {}): Promise<Response> {
  return fetch(input, init).catch(() => {
    throw httpError(null, null)
  })
}

// httpError 把一次失败请求归一成面向用户的人话错误对象
// 只透传后端明确给出的业务 message；无 message 时按下述规则兜底，保证穷尽所有分支：
//   - 无响应（网络层失败）提示网络异常
//   - 5xx（含 nginx 网关 HTML 兜底、后端宕机）提示服务暂不可用
//   - 429 限流提示操作过频
//   - 其余（4xx 等）提示操作失败
// res 失败响应，为 null 表示网络层失败；body 已解析的响应体（可能因非 JSON 为 null）
export function httpError(res: Response | null, body: unknown): Error {
  if (!res) {
    return new Error('网络异常，请检查网络后重试')
  }
  const msg = (body as { error?: { message?: string } } | null)?.error?.message
  if (msg) {
    return new Error(msg)
  }
  if (res.status >= 500) {
    return new Error('服务暂时不可用，请稍后重试')
  }
  if (res.status === 429) {
    return new Error('操作过于频繁，请稍后再试')
  }
  return new Error('操作失败，请稍后重试')
}