import type { AITogglePayload, LeavePayload, OutgoingSignalingMessage, SignalingMessage } from '../types/signaling'

// 信令心跳间隔要短于常见代理 60 秒空闲超时，避免 WebSocket 长时间无数据被中间层关闭
const SIGNALING_HEARTBEAT_INTERVAL_MS = 25_000
// 发出 ping 后如果超过该时间仍未收到 pong，认为连接已死，主动关闭以触发重连
const SIGNALING_HEARTBEAT_TIMEOUT_MS = 10_000

// 默认重连策略：最多 10 次，首次 1 秒，按 2 倍指数退避，上限 30 秒
const DEFAULT_RECONNECT_ENABLED = true
const DEFAULT_RECONNECT_MAX_ATTEMPTS = 10
const DEFAULT_RECONNECT_INITIAL_DELAY_MS = 1_000
const DEFAULT_RECONNECT_MAX_DELAY_MS = 30_000
const DEFAULT_RECONNECT_BACKOFF_MULTIPLIER = 2

// SignalingClientHandlers 是信令 WebSocket 生命周期事件的回调集合
export interface SignalingClientHandlers {
  onOpen?: () => void // 信令服务器连接成功或重连成功后的回调
  onMessage: (message: SignalingMessage) => void // 收到信令服务器消息后的回调
  onError?: (error: Event) => void // 信令 WebSocket 连接失败或异常时的回调
  onClose?: (event: CloseEvent) => void // 信令 WebSocket 最终关闭且不再重连时的回调
  onReconnecting?: (attempt: number, maxAttempts: number) => void // 开始一次重连尝试前的回调
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
  handlers: SignalingClientHandlers // 信令 WebSocket 的事件处理函数
  reconnect?: SignalingClientReconnectOptions // 可选的断线重连策略
}

// getDefaultSignalingUrl 根据当前页面协议生成同源信令地址，开发环境由 Vite 代理，生产环境由 Nginx 转发
export function getDefaultSignalingUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws`
}

// SignalingClient 封装和信令服务器之间的 WebSocket 通信，不直接处理 WebRTC 业务
export class SignalingClient {
  private readonly roomId: string // 当前信令连接所属房间
  private readonly username: string // 当前信令连接对应的用户显示名
  private readonly wsUrl: string // 实际连接的信令服务器 WebSocket 地址
  private readonly handlers: SignalingClientHandlers // 上层传入的事件处理函数
  private readonly reconnectOptions: Required<SignalingClientReconnectOptions> // 重连策略的完整配置
  private ws: WebSocket | null = null // 当前 WebSocket 实例，未连接或已关闭时为 null
  private heartbeatTimerId: number | null = null // 浏览器定时发送业务 ping 的计时器 ID
  private heartbeatTimeoutId: number | null = null // 等待服务端 pong 回应的超时计时器 ID
  private reconnectTimerId: number | null = null // 重连定时器 ID
  private reconnectAttempts = 0 // 当前已连续重试的次数，连接成功后清零
  private intentionallyClosed = false // 标记是否由上层主动调用 close，主动关闭后不再重连

  constructor(options: SignalingClientOptions) {
    this.roomId = options.roomId
    this.username = options.username
    this.wsUrl = options.url || getDefaultSignalingUrl()
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

  // connect 建立信令服务器 WebSocket 连接，并绑定浏览器 WebSocket 事件
  // 异常断线后会在内部按指数退避自动重连；上层通过 onReconnecting / onOpen / onClose 感知状态
  connect(): WebSocket {
    console.log('正在连接信令 WebSocket:', {
      wsUrl: this.wsUrl,
      roomId: this.roomId,
      username: this.username,
      attempt: this.reconnectAttempts,
    })

    const ws = new WebSocket(this.wsUrl)
    this.ws = ws

    ws.onopen = () => {
      console.log('信令 WebSocket 已连接:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        username: this.username,
      })
      // 连接成功后重置重连计数并清理重连定时器
      this.reconnectAttempts = 0
      this.stopReconnect()
      this.startHeartbeat()
      this.handlers.onOpen?.()
    }

    ws.onmessage = event => {
      console.log('收到信令 WebSocket 消息:', {
        wsUrl: this.wsUrl,
        data: event.data,
      })
      const message = JSON.parse(event.data) as SignalingMessage
      if (message.type === 'pong') {
        console.log('收到信令 WebSocket pong:', {
          wsUrl: this.wsUrl,
          roomId: this.roomId,
          time: new Date().toISOString(),
        })
        this.stopHeartbeatTimeout()
        return
      }
      this.handlers.onMessage(message)
    }

    ws.onerror = error => {
      // onerror 不会暴露太多底层原因，因此把信令服务器地址和当前房间一起打出来方便和后端/Nginx 日志对齐
      console.error('信令 WebSocket 连接失败:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        readyState: ws.readyState,
        time: new Date().toISOString(),
        error,
      })
      this.stopHeartbeat()
      // 重连过程中不再向上层抛 error，避免 UI 闪烁；最终失败会由 onClose 通知
      if (!this.isReconnecting()) {
        this.handlers.onError?.(error)
      }
    }

    ws.onclose = event => {
      // 记录浏览器能拿到的关闭细节，排查代理超时或异常断开时重点看 code 和 wasClean
      console.log('信令 WebSocket 已关闭:', {
        wsUrl: this.wsUrl,
        code: event.code,
        reason: event.reason || '(空)',
        wasClean: event.wasClean,
        readyState: ws.readyState,
        roomId: this.roomId,
        time: new Date().toISOString(),
      })
      this.stopHeartbeat()
      if (this.ws === ws) {
        this.ws = null
      }

      // 主动关闭时静默清理，上层已在 leaveRoom / 卸载流程中重置状态，无需再通知 onClose
      if (this.intentionallyClosed) {
        return
      }

      // 如果已经有新的连接在进行中（例如网络恢复后立刻触发重连），忽略旧连接的关闭事件
      if (this.ws !== null && this.ws.readyState !== WebSocket.CLOSED) {
        return
      }

      // 异常关闭时进入自动重连流程
      this.scheduleReconnect()
    }

    return ws
  }

  // send 把结构化信令消息序列化后发给信令服务器
  send<TPayload>(message: OutgoingSignalingMessage<TPayload>): void {
    if (this.ws?.readyState !== WebSocket.OPEN) {
      console.warn('信令 WebSocket 未打开，跳过消息:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        readyState: this.ws?.readyState,
        messageType: message.type,
      })
      return
    }

    this.ws.send(JSON.stringify(message))
  }

  // sendJoin 告诉信令服务器当前用户要进入哪个房间
  sendJoin(): void {
    this.send({
      type: 'join',
      room_id: this.roomId,
      payload: this.username,
    })
  }

  // sendPing 发送业务层心跳，保活信令 WebSocket
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

  // sendLeave 告诉服务端当前用户要离开房间
  // 房主离开时 payload 可携带 next_host_id；普通成员离开时不需要 payload
  sendLeave(payload?: LeavePayload): void {
    this.send({
      type: 'leave',
      room_id: this.roomId,
      payload,
    })
  }

  // close 主动关闭信令 WebSocket，通常在用户离开房间或组件卸载时调用
  // 主动关闭会取消任何进行中的重连，避免离开后仍继续尝试连接
  close(): void {
    this.intentionallyClosed = true
    this.stopReconnect()
    this.stopHeartbeat()
    this.ws?.close()
    this.ws = null
    window.removeEventListener('online', this.handleOnline)
    window.removeEventListener('offline', this.handleOffline)
  }

  // startHeartbeat 在信令连接成功后定时发送业务 ping，避免代理层认为连接空闲
  // 连接建立后立即发送一次 ping，以便尽快启动 pong 超时检测
  private startHeartbeat(): void {
    this.stopHeartbeat()
    this.sendPing()
    this.heartbeatTimerId = window.setInterval(() => {
      this.sendPing()
    }, SIGNALING_HEARTBEAT_INTERVAL_MS)
  }

  // stopHeartbeat 停止业务心跳，避免连接关闭后计时器继续运行
  private stopHeartbeat(): void {
    if (this.heartbeatTimerId === null) return

    window.clearInterval(this.heartbeatTimerId)
    this.heartbeatTimerId = null
    // 停止发送循环时同时清理等待中的 pong 超时，避免误触发
    this.stopHeartbeatTimeout()
  }

  // startHeartbeatTimeout 在发送 ping 后启动超时检测
  // 如果超时仍未收到 pong，主动关闭 WebSocket 以尽快触发重连流程
  private startHeartbeatTimeout(): void {
    this.stopHeartbeatTimeout()
    this.heartbeatTimeoutId = window.setTimeout(() => {
      console.warn('信令 WebSocket 心跳超时，主动关闭连接触发重连:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        time: new Date().toISOString(),
      })
      this.ws?.close()
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
      console.error('信令 WebSocket 重连次数耗尽:', {
        wsUrl: this.wsUrl,
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

    console.log(`信令 WebSocket 将在 ${delay}ms 后进行第 ${this.reconnectAttempts}/${this.reconnectOptions.maxAttempts} 次重连`, {
      wsUrl: this.wsUrl,
      roomId: this.roomId,
    })
    this.handlers.onReconnecting?.(this.reconnectAttempts, this.reconnectOptions.maxAttempts)

    this.reconnectTimerId = window.setTimeout(() => {
      this.reconnectTimerId = null
      this.connect()
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
    if (this.ws?.readyState === WebSocket.OPEN || this.ws?.readyState === WebSocket.CONNECTING) return
    if (!this.isReconnecting()) return

    console.log('网络已恢复，立即尝试重连:', {
      wsUrl: this.wsUrl,
      roomId: this.roomId,
      time: new Date().toISOString(),
    })
    this.stopReconnect()
    this.connect()
  }

  // handleOffline 在浏览器感知到网络断开时触发
  // 如果当前有打开的连接，主动关闭以尽快进入重连流程，避免等 TCP 超时
  private handleOffline = (): void => {
    if (this.intentionallyClosed || this.ws?.readyState !== WebSocket.OPEN) return

    console.log('网络已断开，主动关闭 WebSocket 触发重连:', {
      wsUrl: this.wsUrl,
      roomId: this.roomId,
      time: new Date().toISOString(),
    })
    this.ws.close()
  }
}
