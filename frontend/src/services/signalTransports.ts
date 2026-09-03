// signalTransports.ts 信令传输适配层：WebSocket 与 WebTransport 双通道统一封装
//
// 选型动机：WebTransport(HTTP/3 over QUIC) 相比 WebSocket 拥有独立的 UDP 拥塞控制、
// 0-RTT 会话恢复与多路复用，是语音场景上优于 TCP 的下行信道（不头阻塞）
// 策略：优先 WebTransport，握手失败/证书不可信/浏览器不支持时自动降级 WebSocket
// 帧协议：与后端 wt 包一致，每条消息 4 字节大端长度前缀 + JSON 体
// 兼容性：浏览器 WebTransport 仅支持受信证书（HTTPS context），开发期自签证书走降级路径
import { getAccessToken } from './auth'

// KICKED_MESSAGE 后端踢下线（会话吊销/封禁）时的应用消息类型，与后端 MsgTypeKicked 对应
// WebTransport 无 WebSocket 关闭码语义，踢人靠该应用消息触达前端
export const KICKED_MESSAGE = 'kicked'

// MAX_FRAME_SIZE 单帧字节数上限，防止恶意服务端用超大长度头拖垮内存
const MAX_FRAME_SIZE = 1 << 20

// WT_HANDSHAKE_TIMEOUT_MS WebTransport 会话握手（QUIC+TLS+鉴权）的超时窗口
// 超时判降级：自签证书会触发 TLS 校验失败，通常远快于该超时，此处兜底 QUIC 黑洞
const WT_HANDSHAKE_TIMEOUT_MS = 3_000

// TransportHandlers 传输层事件的回调集合
// onClose 收到底层连接关闭通知（WS 传出 code，WT 固定 undefined）；onFallback 仅 WebTransport 使用
export interface TransportHandlers {
  onOpen: () => void // 底层通道建立成功（WS onopen / WT 双向流就绪）
  onMessage: (text: string) => void // 收到一帧完整消息（JSON 原文）
  onError: (error: unknown) => void // 底层通道异常（WS onerror / WT 会话错误）
  onClose: (code?: number) => void // 底层通道关闭（异常或主动）
  onFallback: () => void // WebTransport 不可用时请求切换到 WebSocket
}

// SignalTransport 信令传输抽象：SignalingClient 只依赖该接口，不感知具体通道
export interface SignalTransport {
  readonly kind: 'wt' | 'ws' // 传输通道类型，用于日志与降级决策
  connect: () => void // 建立底层连接并开始收发消息
  send: (text: string) => void // 发送一帧完整消息（连接的 JSON 原文）
  close: () => void // 关闭底层连接，触发 onClose
  isOpen: () => boolean // 是否存在进行中的连接（OPEN/CONNECTING 或 QUIC 会话存活）
}

// getDefaultWebTransportUrl 生成同主机 WebTransport 信令地址
// 端口与后端 wt.Addr 默认一致（4433）；生产可通过 options.webTransportUrl 覆盖
export function getDefaultWebTransportUrl(): string {
  const host = window.location.hostname || 'localhost'
  return `https://${host}:4433/.well-known/webtransport/signal`
}

// withAccessToken 把 access 附加到传输地址：WebSocket 与 WebTransport 都无法自定义 Header
// 浏览器握手鉴权只能走 query，后端要求 ?token=（spec §9.1），与 WebSocket 实现保持一致
export function withAccessToken(url: string): string {
  const token = getAccessToken()
  return token ? `${url}${url.includes('?') ? '&' : '?'}token=${encodeURIComponent(token)}` : url
}

// WebSocketSignalTransport WebSocket 通道实现，字段与方法对齐浏览器 WebSocket API
class WebSocketSignalTransport implements SignalTransport {
  readonly kind = 'ws' as const // 传输通道类型固定为 ws
  private ws: WebSocket | null = null // 当前 WebSocket 实例，未连接或已关闭时为 null

  constructor(private readonly url: string, private readonly handlers: TransportHandlers) {}

  // connect 建立 WebSocket 连接并绑定事件，连接生命周期由 SignalingClient 统一调度
  connect(): void {
    const ws = new WebSocket(this.url)
    this.ws = ws
    ws.onopen = () => this.handlers.onOpen()
    ws.onmessage = event => this.handlers.onMessage(String(event.data))
    ws.onerror = error => this.handlers.onError(error)
    ws.onclose = event => {
      // 新连接已建立时忽略旧连接的迟达关闭事件，避免误触发重连
      if (this.ws !== ws) return
      this.ws = null
      this.handlers.onClose(event.code)
    }
  }

  // send 仅在连接打开时写出消息，未连接时静默丢弃（与原有 WS 行为一致）
  send(text: string): void {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(text)
  }

  // close 主动关闭 WebSocket，底层 onclose 回调由 SignalingClient 处理重连决策
  close(): void {
    this.ws?.close()
  }

  // isOpen 判断是否存在进行中的连接，供网络恢复时避免重复触发连接
  isOpen(): boolean {
    return this.ws?.readyState === WebSocket.OPEN || this.ws?.readyState === WebSocket.CONNECTING
  }
}

// decodeText 把 Uint8Array 解码为字符串，供帧解码器复用
function decodeText(bytes: Uint8Array): string {
  return new TextDecoder().decode(bytes)
}

// FrameDecoder 按后端帧协议把连续的字节流切分成完整消息帧
// 累积缓冲 + 逐帧弹出：兼容 QUIC 双向流的任意分片到达顺序
export class FrameDecoder {
  private buf: Uint8Array = new Uint8Array(0) // 已到达但尚未组成完整帧的字节

  // push 注入新到达的字节块，返回其中所有完整帧的解码文本
  // 首 4 字节为大端长度，长度超限或非法时清空缓冲避免卡死
  push(chunk: Uint8Array): string[] {
    this.buf = concatBytes(this.buf, chunk)
    const frames: string[] = []
    for (;;) {
      if (this.buf.length < 4) return frames
      const len = readUint32(this.buf, 0)
      if (len === 0 || len > MAX_FRAME_SIZE) {
        this.buf = new Uint8Array(0)
        return frames
      }
      if (this.buf.length < 4 + len) return frames
      frames.push(decodeText(this.buf.subarray(4, 4 + len)))
      this.buf = this.buf.slice(4 + len)
    }
  }
}

// WebTransportSignalTransport WebTransport(QUIC) 通道实现：优先走 UDP 信道
// 握手异步：ready 决议后打开双向流；超时、鉴权失败、证书不可信均触发 onFallback 降级
class WebTransportSignalTransport implements SignalTransport {
  readonly kind = 'wt' as const // 传输通道类型固定为 wt
  private wt: BrowserWebTransport | null = null // 当前 WebTransport 会话
  private streamWriter: WritableStreamDefaultWriter<Uint8Array> | null = null // 双向流写入器
  private decoder = new FrameDecoder() // 下行帧解码器
  private settled = false // 会话是否已确定为成功（打开流），此后关闭事件不再触发降级

  constructor(private readonly url: string, private readonly handlers: TransportHandlers) {}

  // connect 建立 WebTransport 会话：优先尝试 QUIC，失败走 onFallback 由上层切 WebSocket
  connect(): void {
    const Ctor = getWebTransportCtor()
    if (!Ctor) {
      this.handlers.onFallback()
      return
    }
    let wt: BrowserWebTransport
    try {
      wt = new Ctor(this.url)
    } catch (err) {
      this.handlers.onFallback()
      return
    }
    this.wt = wt
    const timer = window.setTimeout(() => this.fallback('QUIC 握手超时'), WT_HANDSHAKE_TIMEOUT_MS)
    // ready 决议表示 QUIC+TLS+HTTP3 会话可用，随后打开双向信令流
    wt.ready
      .then(() => {
        window.clearTimeout(timer)
        if (this.settled) return
        void this.openStream()
      })
      .catch(err => {
        window.clearTimeout(timer)
        if (this.settled) return
        this.fallback(`握手失败: ${String(err)}`)
      })
    // closed 在会话主动关闭或对端断开时决议，reject 表示异常关闭
    wt.closed
      .then(() => {
        if (!this.settled) return // 未成会话的关闭统一由 fallback 处理
        this.handlers.onClose(undefined)
      })
      .catch(() => {
        if (!this.settled) return
        this.handlers.onClose(undefined)
      })
  }

  // openStream 等待首条双向流就绪，启动读取循环后通知上层连接成功
  private async openStream(): Promise<void> {
    try {
      const stream = await this.wt!.createBidirectionalStream()
      this.streamWriter = stream.writable.getWriter()
      void this.readLoop(stream.readable)
      this.settled = true
      this.handlers.onOpen()
    } catch (err) {
      this.fallback(`打开信令流失败: ${String(err)}`)
    }
  }

  // readLoop 持续读取双向流字节并把完整帧转交 SignalingClient
  // 流结束（对端关闭或网络断开）时按异常关闭通知上层
  private async readLoop(readable: ReadableStream<Uint8Array>): Promise<void> {
    const reader = readable.getReader()
    try {
      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        for (const frame of this.decoder.push(value)) {
          this.handlers.onMessage(frame)
        }
      }
    } catch (err) {
      this.handlers.onError(err)
    } finally {
      reader.releaseLock()
    }
    this.handlers.onClose(undefined)
  }

  // send 把一帧消息写入双向流，流未就绪时静默丢弃（与 WS 未打开的行为一致）
  send(text: string): void {
    if (!this.streamWriter) return
    void this.streamWriter.write(encodeFrame(text))
  }

  // close 主动关闭 WebTransport 会话，closed 决议后由 SignalingClient 统一处理
  close(): void {
    this.wt?.close()
  }

  // isOpen 判断是否存在进行中的 QUIC 会话，供网络恢复判断使用
  isOpen(): boolean {
    return this.wt !== null
  }

  // fallback 记录降级原因并请求上层切换到 WebSocket 通道
  private fallback(reason: string): void {
    if (this.settled) return
    console.warn('[signaling] WebTransport 不可用，降级 WebSocket:', reason)
    this.wt?.close()
    this.handlers.onFallback()
  }
}

// createPreferredTransport 按环境选择主要通道：浏览器支持且未被禁用时优先 WebTransport
// preferWebTransport 为 false 或浏览器不支持时直接返回 WebSocket 实现
export function createPreferredTransport(
  url: string,
  handlers: TransportHandlers,
  preferWebTransport = true,
): SignalTransport {
  if (preferWebTransport) {
    return new WebTransportSignalTransport(withAccessToken(url), handlers)
  }
  return new WebSocketSignalTransport(withAccessToken(url), handlers)
}

// encodeFrame 把一帧消息编码为 4 字节大端长度前缀 + JSON 字节，供双向流写出
export function encodeFrame(text: string): Uint8Array {
  const body = new TextEncoder().encode(text)
  const frame = new Uint8Array(4 + body.byteLength)
  new DataView(frame.buffer).setUint32(0, body.byteLength)
  frame.set(body, 4)
  return frame
}

// readUint32 读缓冲起始 4 字节的大端无符号整数
// 首字节用乘法而非移位避免 32 位有符号溢出：移位会让 >=0x80000000 的长度头变负数，绕过超限检测
function readUint32(bytes: Uint8Array, offset: number): number {
  return (
    bytes[offset] * 0x1000000 +
    (bytes[offset + 1] << 16) +
    (bytes[offset + 2] << 8) +
    bytes[offset + 3]
  )
}

// concatBytes 拼接两段字节缓冲，返回新 Uint8Array
function concatBytes(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.byteLength + b.byteLength)
  out.set(a, 0)
  out.set(b, a.byteLength)
  return out
}

// getWebTransportCtor 从 window 读取浏览器原生 WebTransport 构造器，缺失表示环境不支持
function getWebTransportCtor(): (new (url: string) => BrowserWebTransport) | null {
  const ctor = (window as unknown as { WebTransport?: new (url: string) => BrowserWebTransport }).WebTransport
  return ctor ?? null
}

// BrowserWebTransport 浏览器 WebTransport 的最小类型面，避免依赖 TS 版本的 DOM lib 演进
interface BrowserWebTransport {
  ready: Promise<undefined> // 会话建立成功后的 Promise
  closed: Promise<{ error: DOMException | null }> // 会话关闭后的 Promise
  createBidirectionalStream: () => Promise<WtStream> // 打开首条/下一条双向流
  close: () => void // 主动关闭会话
}

// WtStream WebTransport 双向流的一对读写侧
interface WtStream {
  readable: ReadableStream<Uint8Array> // 下行数据流（服务端 → 客户端）
  writable: WritableStream<Uint8Array> // 上行数据流（客户端 → 服务端）
}