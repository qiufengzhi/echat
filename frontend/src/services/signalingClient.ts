import type { AITogglePayload, LeavePayload, ManageMicPayload, OutgoingSignalingMessage, SignalingMessage } from '../types/signaling'
import { refresh } from './auth'
import {
  KICKED_MESSAGE,
  createPreferredTransport,
  getDefaultWebTransportUrl,
  type SignalTransport,
  type TransportHandlers,
} from './signalTransports'

// 信令心跳间隔要短于常见代理 60 秒空闲超时，避免信令通道长时间无数据被中间层关闭
const SIGNALING_HEARTBEAT_INTERVAL_MS = 25_000
// 发出 ping 后如果超过该时间仍未收到 pong，认为连接已死，主动关闭以触发重连
const SIGNALING_HEARTBEAT_TIMEOUT_MS = 10_000

// KICKED_CLOSE_CODE 服务端主动踢下线（会话吊销/封禁）使用的自定义关闭码，与后端 room 包保持一致
// WebSocket 通道仍以该关闭码兜底识别；WebTransport 通道无关闭码语义，靠 kicked 应用消息检测
export const KICKED_CLOSE_CODE = 4001

// 默认重连策略：最多 10 次，首次 1 秒，按 2 倍指数退避，上限 30 秒
const DEFAULT_RECONNECT_ENABLED = true
const DEFAULT_RECONNECT_MAX_ATTEMPTS = 10
const DEFAULT_RECONNECT_INITIAL_DELAY_MS = 1_000
const DEFAULT_RECONNECT_MAX_DELAY_MS = 30_000
const DEFAULT_RECONNECT_BACKOFF_MULTIPLIER = 2

// SignalingClientHandlers 是信令通道生命周期事件的回调集合，与传输通道类型无关
export interface SignalingClientHandlers {
  onOpen?: () => void // 信令连接成功或重连成功后的回调
  onMessage: (message: SignalingMessage) => void // 收到信令服务器消息后的回调
  onError?: (error: Event) => void // 信令连接失败或异常时的回调
  onClose?: (event: CloseEvent) => void // 信令最终关闭且不再重连时的回调
  onReconnecting?: (attempt: number, maxAttempts: number) => void // 开始一次重连尝试前的回调
  onKicked?: () => void // 被服务端强制踢下线（会话吊销/封禁）时的回调
}

// SignalingClientReconnectOptions 配置断线后的自动重连行为
export interface SignalingClientReconnectOptions {
  enabled?: boolean // 是否启用自动重连
  maxAttempts?: number // 最大重连尝试次数
  initialDelayMs?: number // 首次重连等待毫秒数
  maxDelayMs?: number // 重连等待毫秒数上限
  backoffMultiplier?: number // 指数退避乘数
}

// SignalingClientOptions 描述创建信令客户端所需的上下文信息
export interface SignalingClientOptions {
  roomId: string // 当前要加入的房间 ID
  username: string // 当前用户进入房间时使用的显示名称
  url?: string // 可选的信令服务器地址；默认使用当前页面同源 /ws
  webTransportUrl?: string // 可选的 WebTransport 高优先级通道地址；默认同主机 :4433
  preferWebTransport?: boolean // 浏览器支持时是否优先 WebTransport(QUIC)，默认 true
  handlers: SignalingClientHandlers // 信令通道的事件处理函数
  reconnect?: SignalingClientReconnectOptions // 可选的断线重连策略
}

// getDefaultSignalingUrl 根据当前页面协议生成同源信令地址，开发环境由 Vite 代理，生产环境由 Nginx 转发
export function getDefaultSignalingUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws`
}

// SignalingClient 封装信令服务器双通道通信：WebTransport(QUIC) 优先，失败自动降级 WebSocket
// 内部持有传输适配层（SignalTransport），对上层暴露与通道无关的生命周期与消息方法
export class SignalingClient {
  private readonly roomId: string // 当前信令连接所属房间
  private readonly username: string // 当前信令连接对应的用户显示名
  private readonly wsUrl: string // 降级通道（WebSocket）地址
  private readonly wtUrl: string // 首选通道（WebTransport）地址
  private readonly preferWebTransport: boolean // 是否启用 WebTransport 优先策略
  private readonly handlers: SignalingClientHandlers // 上层传入的事件处理函数
  private readonly reconnectOptions: Required<SignalingClientReconnectOptions> // 重连策略的完整配置
  private transport: SignalTransport | null = null // 当前使用的信令传输通道
  private heartbeatTimerId: number | null = null // 浏览器定时发送业务 ping 的计时器 ID
  private heartbeatTimeoutId: number | null = null // 等待服务端 pong 回应的超时计时器 ID
  private reconnectTimerId: number | null = null // 重连定时器 ID
  private reconnectAttempts = 0 // 当前已连续重试的次数，连接成功后清零
  private intentionallyClosed = false // 标记是否由上层主动调用 close，主动关闭后不再重连

  constructor(options: SignalingClientOptions) {
    this.roomId = options.roomId
    this.username = options.username
    this.wsUrl = options.url || getDefaultSignalingUrl()
    this.wtUrl = options.webTransportUrl || getDefaultWebTransportUrl()
    this.preferWebTransport = options.preferWebTransport ?? true
    this.handlers = options.handlers
    this.reconnectOptions = {
      enabled: options.reconnect?.enabled ?? DEFAULT_RECONNECT_ENABLED,
      maxAttempts: options.reconnect?.maxAttempts ?? DEFAULT_RECONNECT_MAX_ATTEMPTS,
      initialDelayMs: options.reconnect?.initialDelayMs ?? DEFAULT_RECONNECT_INITIAL_DELAY_MS,
      maxDelayMs: options.reconnect?.maxDelayMs ?? DEFAULT_RECONNECT_MAX_DELAY_MS,
      backoffMultiplier: options.reconnect?.backoffMultiplier ?? DEFAULT_RECONNECT_BACKOFF_MULTIPLIER,
    }

    // 监听浏览器网络状态变化，可以在几百毫秒内感知断网/恢复
    window.addEventListener('online', this.handleOnline)
    window.addEventListener('offline', this.handleOffline)
  }

  // connect 建立信令通道：首次选择 WebTransport 优先通道，重连时复用当前通道
  // 异常断线后会在内部按指数退避自动重连；上层通过 onReconnecting / onOpen / onClose 感知状态
  connect(): void {
    if (!this.transport) {
      this.transport = createPreferredTransport(
        this.preferWebTransport ? this.wtUrl : this.wsUrl,
        this.transportHandlers(),
        this.preferWebTransport,
      )
    }
    this.transport.connect()
  }

  // send 把结构化信令消息序列化后交给当前通道发送
  send<TPayload>(message: OutgoingSignalingMessage<TPayload>): void {
    if (!this.transport) {
      console.warn('信令通道未建立，跳过消息:', {
        roomId: this.roomId,
        messageType: message.type,
      })
      return
    }
    this.transport.send(JSON.stringify(message))
  }

  // sendJoin 告诉信令服务器当前用户要进入哪个房间
  sendJoin(): void {
    this.send({
      type: 'join',
      room_id: this.roomId,
      payload: this.username,
    })
  }

  // sendPing 发送业务层心跳，保活信令通道
  // 发送后会启动 pong 超时检测，避免移动端断网时浏览器长时间不触发 close 事件
  sendPing(): void {
    this.send({
      type: 'ping',
      room_id: this.roomId,
    })
    this.startHeartbeatTimeout()
  }

  // --- SFU 信令方法 ---
  // 客户端发起 SDP Offer，服务端回复 Answer

  // sendSFUOffer 把本端创建的 SDP Offer 发给 SFU 服务端以启动 SDP 协商
  sendSFUOffer(offer: RTCSessionDescriptionInit): void {
    this.send({
      type: 'sfu_offer',
      room_id: this.roomId,
      payload: offer,
    })
  }

  // sendSFUIce 把浏览器发现的 ICE Candidate 发给 SFU 服务端
  sendSFUIce(candidate: RTCIceCandidate): void {
    this.send({
      type: 'sfu_ice',
      room_id: this.roomId,
      payload: {
        candidate: candidate.candidate,
        sdpMLineIndex: candidate.sdpMLineIndex,
        sdpMid: candidate.sdpMid,
        usernameFragment: candidate.usernameFragment,
      },
    })
  }

  // sendRenegotiationAnswer 把客户端对 renegotiation Offer 的 Answer 发给 SFU 服务端
  sendRenegotiationAnswer(answer: RTCSessionDescriptionInit): void {
    this.send({
      type: 'sfu_renegotiation_answer',
      room_id: this.roomId,
      payload: answer,
    })
  }

  // sendAIToggle 请求切换 AI 语音助手的开关状态，仅房主可以调用，服务端以 ai_status 回复确认
  sendAIToggle(enable: boolean): void {
    this.send<AITogglePayload>({
      type: 'ai_toggle',
      room_id: this.roomId,
      payload: { enable },
    })
  }

  // --- 成员管理信令 ---
  // 服务端广播会排除操作者自身，前端在本侧做乐观更新，信令只负责让他人看到变更

  // sendRaiseHand 听众举手请求上麦，消息无载荷，服务端广播 hand_raised
  sendRaiseHand(): void {
    this.send({
      type: 'raise_hand',
      room_id: this.roomId,
    })
  }

  // sendApproveMic 房主/副主持批准目标成员上麦，服务端广播 mic_approved
  sendApproveMic(targetUserId: string): void {
    this.send<ManageMicPayload>({
      type: 'approve_mic',
      room_id: this.roomId,
      payload: { target_user_id: targetUserId },
    })
  }

  // sendRejectMic 房主/副主持拒绝目标成员举手，服务端定向通知被拒者 mic_rejected
  sendRejectMic(targetUserId: string): void {
    this.send<ManageMicPayload>({
      type: 'reject_mic',
      room_id: this.roomId,
      payload: { target_user_id: targetUserId },
    })
  }

  // sendKickMic 房主/副主持请目标成员（speaker）下麦，服务端广播 mic_kicked
  sendKickMic(targetUserId: string): void {
    this.send<ManageMicPayload>({
      type: 'kick_mic',
      room_id: this.roomId,
      payload: { target_user_id: targetUserId },
    })
  }

  // sendMuteMic 房主/副主持静音或解除静音目标成员，服务端广播 muted
  sendMuteMic(targetUserId: string, muted: boolean): void {
    this.send<ManageMicPayload>({
      type: 'mute_mic',
      room_id: this.roomId,
      payload: { target_user_id: targetUserId, muted },
    })
  }

  // sendLeave 告诉服务端当前用户要离开房间
  // 房主离开时 payload 可携带 next_host_id；普通成员离开时不需要 payload
  sendLeave(payload?: LeavePayload): void {
    this.send({
      type: 'leave',
      room_id: this.roomId,
      payload,
    })
  }

  // close 主动关闭信令通道，通常在用户离开房间或组件卸载时调用
  // 主动关闭会取消任何进行中的重连，避免离开后仍继续尝试连接
  close(): void {
    this.intentionallyClosed = true
    this.stopReconnect()
    this.stopHeartbeat()
    this.transport?.close()
    this.transport = null
    window.removeEventListener('online', this.handleOnline)
    window.removeEventListener('offline', this.handleOffline)
  }

  // transportHandlers 生成绑定到当前通道的事件转发集合
  // onFallback 仅在 WebTransport 通道内触发：未能建立 QUIC 会话时切到 WebSocket
  private transportHandlers(): TransportHandlers {
    return {
      onOpen: () => this.handleTransportOpen(),
      onMessage: text => this.handleTransportMessage(text),
      onError: error => this.handleTransportError(error),
      onClose: code => this.handleTransportClose(code),
      onFallback: () => this.fallbackToWebSocket(),
    }
  }

  // handleTransportOpen 通道连接成功后统一重置重连状态并启动心跳
  private handleTransportOpen(): void {
    console.log('[signaling] 信令通道已连接', { roomId: this.roomId, kind: this.transport?.kind })
    this.reconnectAttempts = 0
    this.stopReconnect()
    this.startHeartbeat()
    this.handlers.onOpen?.()
  }

  // handleTransportMessage 统一的收帧入口：pong 心响应答、踢下线消息、业务消息分派
  // kicked 通道无关的应用消息（WebTransport 无关闭码），收到即视为强制下线
  private handleTransportMessage(text: string): void {
    const message = JSON.parse(text) as SignalingMessage
    if (message.type === 'pong') {
      this.stopHeartbeatTimeout()
      return
    }
    if (message.type === KICKED_MESSAGE) {
      console.warn('[signaling] 信令通道被服务端踢下线', { roomId: this.roomId })
      this.intentionallyClosed = true
      this.stopHeartbeat()
      this.handlers.onKicked?.()
      return
    }
    this.handlers.onMessage(message)
  }

  // handleTransportError 通道异常时的统一处理；重连中不重复向上层抛错误避免 UI 闪烁
  private handleTransportError(error: unknown): void {
    console.error('[signaling] 信令通道错误', { roomId: this.roomId, kind: this.transport?.kind, error })
    this.stopHeartbeat()
    if (!this.isReconnecting()) {
      this.handlers.onError?.(error as Event)
    }
  }

  // handleTransportClose 通道关闭后的统一决策：主动关闭静默、踢下线交给上层、异常走重连
  private handleTransportClose(code?: number): void {
    this.stopHeartbeat()

    // 主动关闭时静默清理，上层已在 leaveRoom / 卸载流程中重置状态，无需再通知 onClose
    if (this.intentionallyClosed) {
      return
    }

    // WebSocket 通道兜底的踢下线识别：应用消息先到则 intentionallyClosed 已置位，此处直接返回
    if (code === KICKED_CLOSE_CODE) {
      console.warn('[signaling] 信令通道被服务端踢下线（关闭码）', { code })
      this.intentionallyClosed = true
      this.handlers.onKicked?.()
      return
    }

    this.scheduleReconnect()
  }

  // fallbackToWebSocket WebTransport 首选通道不可用时降级到 WebSocket 并立即连接
  private fallbackToWebSocket(): void {
    console.log('[signaling] WebTransport 不可用，降级 WebSocket', { roomId: this.roomId })
    this.transport = createPreferredTransport(this.wsUrl, this.transportHandlers(), false)
    this.transport.connect()
  }

  // startHeartbeat 在信令通道连接成功后定时发送业务 ping，避免代理层认为连接空闲
  // 连接建立后立即发送一次 ping，以便尽快启动 pong 超时检测
  private startHeartbeat(): void {
    this.stopHeartbeat()
    this.sendPing()
    this.heartbeatTimerId = window.setInterval(() => {
      this.sendPing()
    }, SIGNALING_HEARTBEAT_INTERVAL_MS)
  }

  // stopHeartbeat 停止业务心跳，避免通道关闭后计时器继续运行
  private stopHeartbeat(): void {
    if (this.heartbeatTimerId === null) return

    window.clearInterval(this.heartbeatTimerId)
    this.heartbeatTimerId = null
    // 停止发送循环时同时清理等待中的 pong 超时，避免误触发
    this.stopHeartbeatTimeout()
  }

  // startHeartbeatTimeout 在发送 ping 后启动超时检测
  // 如果超时仍未收到 pong，主动关闭通道以尽快触发重连流程
  private startHeartbeatTimeout(): void {
    this.stopHeartbeatTimeout()
    this.heartbeatTimeoutId = window.setTimeout(() => {
      console.warn('[signaling] 信令心跳超时，主动关闭通道触发重连', {
        roomId: this.roomId,
        kind: this.transport?.kind,
      })
      this.transport?.close()
    }, SIGNALING_HEARTBEAT_TIMEOUT_MS)
  }

  // stopHeartbeatTimeout 取消尚未触发的 pong 超时检测
  private stopHeartbeatTimeout(): void {
    if (this.heartbeatTimeoutId === null) return

    window.clearTimeout(this.heartbeatTimeoutId)
    this.heartbeatTimeoutId = null
  }

  // isReconnecting 返回当前是否处于断线重连等待中
  private isReconnecting(): boolean {
    return this.reconnectTimerId !== null
  }

  // scheduleReconnect 按指数退避策略安排下一次重连
  // 达到最大次数时触发 onClose 通知上层彻底失败
  private scheduleReconnect(): void {
    if (!this.reconnectOptions.enabled) {
      this.handlers.onClose?.(new CloseEvent('close', { code: 1006, reason: '重连已禁用' }))
      return
    }

    if (this.reconnectAttempts >= this.reconnectOptions.maxAttempts) {
      console.error('[signaling] 信令重连次数耗尽', {
        roomId: this.roomId,
        maxAttempts: this.reconnectOptions.maxAttempts,
      })
      this.handlers.onClose?.(new CloseEvent('close', { code: 1006, reason: '重连次数耗尽' }))
      return
    }

    this.reconnectAttempts++
    const delay = Math.min(
      this.reconnectOptions.initialDelayMs * this.reconnectOptions.backoffMultiplier ** (this.reconnectAttempts - 1),
      this.reconnectOptions.maxDelayMs,
    )

    console.log(`[signaling] 信令通道将在 ${delay}ms 后进行第 ${this.reconnectAttempts}/${this.reconnectOptions.maxAttempts} 次重连`, {
      roomId: this.roomId,
      kind: this.transport?.kind,
    })
    this.handlers.onReconnecting?.(this.reconnectAttempts, this.reconnectOptions.maxAttempts)

    this.reconnectTimerId = window.setTimeout(() => {
      this.reconnectTimerId = null
      // access 短命：重连前先刷新一次，避免握手 401 形成的重连死循环
      void refresh().finally(() => this.connect())
    }, delay)
  }

  // stopReconnect 取消尚未执行的重连定时器
  private stopReconnect(): void {
    if (this.reconnectTimerId === null) return

    window.clearTimeout(this.reconnectTimerId)
    this.reconnectTimerId = null
  }

  // handleOnline 在浏览器感知到网络恢复时触发
  // 如果当前正在等待重连且没有进行中的连接，立即跳过剩余退避时间尝试连接
  private handleOnline = (): void => {
    if (this.transport?.isOpen()) return
    if (!this.isReconnecting()) return

    console.log('[signaling] 网络已恢复，立即尝试重连:', {
      roomId: this.roomId,
      time: new Date().toISOString(),
    })
    this.stopReconnect()
    this.connect()
  }

  // handleOffline 在浏览器感知到网络断开时触发
  // 如果当前有打开的连接，主动关闭以尽快进入重连流程，避免等 TCP/QUIC 超时
  private handleOffline = (): void => {
    if (this.intentionallyClosed || !this.transport?.isOpen()) return

    console.log('[signaling] 网络已断开，主动关闭信令通道触发重连:', {
      roomId: this.roomId,
      time: new Date().toISOString(),
    })
    this.transport.close()
  }
}