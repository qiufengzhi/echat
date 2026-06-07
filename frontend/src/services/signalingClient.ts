import type { OutgoingSignalingMessage, SignalingMessage } from '../types/signaling'

// 信令心跳间隔要短于常见代理 60 秒空闲超时，避免 WebSocket 长时间无数据被中间层关闭。
const SIGNALING_HEARTBEAT_INTERVAL_MS = 25_000

// SignalingClientHandlers 是信令 WebSocket 生命周期事件的回调集合。
export interface SignalingClientHandlers {
  onOpen?: () => void // 信令服务器连接成功后的回调。
  onMessage: (message: SignalingMessage) => void // 收到信令服务器消息后的回调。
  onError?: (error: Event) => void // 信令 WebSocket 连接失败或异常时的回调。
  onClose?: (event: CloseEvent) => void // 信令 WebSocket 关闭时的回调。
}

// SignalingClientOptions 描述创建信令客户端所需的上下文信息。
export interface SignalingClientOptions {
  roomId: string // 当前要加入的房间 ID。
  username: string // 当前用户进入房间时使用的显示名称。
  url?: string // 可选的信令服务器地址；默认使用当前页面同源 /ws。
  handlers: SignalingClientHandlers // 信令 WebSocket 的事件处理函数。
}

// getDefaultSignalingUrl 根据当前页面协议生成同源信令地址，开发环境由 Vite 代理，生产环境由 Nginx 转发。
export function getDefaultSignalingUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws`
}

// SignalingClient 封装和信令服务器之间的 WebSocket 通信，不直接处理 WebRTC 业务。
export class SignalingClient {
  private readonly roomId: string // 当前信令连接所属房间。
  private readonly username: string // 当前信令连接对应的用户显示名。
  private readonly wsUrl: string // 实际连接的信令服务器 WebSocket 地址。
  private readonly handlers: SignalingClientHandlers // 上层传入的事件处理函数。
  private ws: WebSocket | null = null // 当前 WebSocket 实例，未连接或已关闭时为 null。
  private heartbeatTimerId: number | null = null // 浏览器定时发送业务 ping 的计时器 ID。

  constructor(options: SignalingClientOptions) {
    this.roomId = options.roomId
    this.username = options.username
    this.wsUrl = options.url || getDefaultSignalingUrl()
    this.handlers = options.handlers
  }

  // connect 建立信令服务器 WebSocket 连接，并绑定浏览器 WebSocket 事件。
  connect(): WebSocket {
    console.log('Connecting signaling WebSocket:', {
      wsUrl: this.wsUrl,
      roomId: this.roomId,
      username: this.username,
    })

    const ws = new WebSocket(this.wsUrl)
    this.ws = ws

    ws.onopen = () => {
      console.log('Signaling WebSocket connected:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        username: this.username,
      })
      this.startHeartbeat()
      this.handlers.onOpen?.()
    }

    ws.onmessage = event => {
      console.log('Received signaling WebSocket message:', {
        wsUrl: this.wsUrl,
        data: event.data,
      })
      const message = JSON.parse(event.data) as SignalingMessage
      if (message.type === 'pong') {
        console.log('Received signaling WebSocket pong:', {
          wsUrl: this.wsUrl,
          roomId: this.roomId,
          time: new Date().toISOString(),
        })
        return
      }
      this.handlers.onMessage(message)
    }

    ws.onerror = error => {
      // onerror 不会暴露太多底层原因，因此把信令服务器地址和当前房间一起打出来方便和后端/Nginx 日志对齐。
      console.error('Signaling WebSocket connection failed:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        readyState: ws.readyState,
        time: new Date().toISOString(),
        error,
      })
      this.stopHeartbeat()
      this.handlers.onError?.(error)
    }

    ws.onclose = event => {
      // 记录浏览器能拿到的关闭细节，排查代理超时或异常断开时重点看 code 和 wasClean。
      console.log('Signaling WebSocket closed:', {
        wsUrl: this.wsUrl,
        code: event.code,
        reason: event.reason || '(empty)',
        wasClean: event.wasClean,
        readyState: ws.readyState,
        roomId: this.roomId,
        time: new Date().toISOString(),
      })
      this.stopHeartbeat()
      if (this.ws === ws) {
        this.ws = null
      }
      this.handlers.onClose?.(event)
    }

    return ws
  }

  // send 把结构化信令消息序列化后发给信令服务器。
  send<TPayload>(message: OutgoingSignalingMessage<TPayload>): void {
    if (this.ws?.readyState !== WebSocket.OPEN) {
      console.warn('Signaling WebSocket is not open, message skipped:', {
        wsUrl: this.wsUrl,
        roomId: this.roomId,
        readyState: this.ws?.readyState,
        messageType: message.type,
      })
      return
    }

    this.ws.send(JSON.stringify(message))
  }

  // sendJoin 告诉信令服务器当前用户要进入哪个房间。
  sendJoin(): void {
    this.send({
      type: 'join',
      room_id: this.roomId,
      payload: this.username,
    })
  }

  // sendPing 发送业务层心跳，保活信令 WebSocket。
  sendPing(): void {
    this.send({
      type: 'ping',
      room_id: this.roomId,
    })
  }

  // sendOffer 把本端创建的 SDP offer 转发给房间内对端。
  sendOffer(offer: RTCSessionDescriptionInit): void {
    this.send({
      type: 'offer',
      room_id: this.roomId,
      payload: offer,
    })
  }

  // sendAnswer 把本端创建的 SDP answer 转发给房间内对端。
  sendAnswer(answer: RTCSessionDescriptionInit): void {
    this.send({
      type: 'answer',
      room_id: this.roomId,
      payload: answer,
    })
  }

  // sendIce 把浏览器发现的 ICE candidate 转发给房间内对端。
  sendIce(candidate: RTCIceCandidate): void {
    this.send({
      type: 'ice',
      room_id: this.roomId,
      payload: candidate,
    })
  }

  // close 主动关闭信令 WebSocket，通常在用户离开房间或组件卸载时调用。
  close(): void {
    this.stopHeartbeat()
    this.ws?.close()
    this.ws = null
  }

  // startHeartbeat 在信令连接成功后定时发送业务 ping，避免代理层认为连接空闲。
  private startHeartbeat(): void {
    this.stopHeartbeat()
    this.heartbeatTimerId = window.setInterval(() => {
      this.sendPing()
    }, SIGNALING_HEARTBEAT_INTERVAL_MS)
  }

  // stopHeartbeat 停止业务心跳，避免连接关闭后计时器继续运行。
  private stopHeartbeat(): void {
    if (this.heartbeatTimerId === null) return

    window.clearInterval(this.heartbeatTimerId)
    this.heartbeatTimerId = null
  }
}
