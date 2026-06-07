import { useState, useEffect, useRef, useCallback } from 'react'

// WebRTC 需要借助 STUN 服务器发现双方可用于直连的公网/局域网候选地址。
const ICE_SERVERS: RTCConfiguration = {
  iceServers: [ // WebRTC 建连时使用的 STUN 服务器列表。
    { urls: 'stun:stun.l.google.com:19302' }, // 主 STUN 地址。
    { urls: 'stun:stun1.l.google.com:19302' }, // 备用 STUN 地址。
  ]
}

// Browsers treat localhost as a secure context exception during local development.
const LOCALHOST_HOSTNAMES = new Set(['localhost', '127.0.0.1', '::1'])

const MICROPHONE_ERROR_MESSAGES = {
  default: '\u65e0\u6cd5\u8bbf\u95ee\u9ea6\u514b\u98ce\uff0c\u8bf7\u68c0\u67e5\u6d4f\u89c8\u5668\u6743\u9650\u548c\u8bbe\u5907\u72b6\u6001', // 通用麦克风错误提示。
  denied: '\u9ea6\u514b\u98ce\u6743\u9650\u88ab\u62d2\u7edd\uff0c\u8bf7\u5728\u6d4f\u89c8\u5668\u5730\u5740\u680f\u4e2d\u5141\u8bb8\u9ea6\u514b\u98ce\u8bbf\u95ee', // 用户拒绝授权时的提示。
  notFound: '\u6ca1\u6709\u68c0\u6d4b\u5230\u53ef\u7528\u9ea6\u514b\u98ce\uff0c\u8bf7\u68c0\u67e5\u8bbe\u5907\u662f\u5426\u5df2\u8fde\u63a5', // 未检测到麦克风时的提示。
  inUse: '\u9ea6\u514b\u98ce\u53ef\u80fd\u88ab\u5176\u4ed6\u5e94\u7528\u5360\u7528\uff0c\u8bf7\u5173\u95ed\u5360\u7528\u540e\u91cd\u8bd5', // 麦克风被占用时的提示。
  constrained: '\u5f53\u524d\u97f3\u9891\u8bbe\u5907\u4e0d\u6ee1\u8db3\u91c7\u96c6\u6761\u4ef6\uff0c\u8bf7\u66f4\u6362\u9ea6\u514b\u98ce\u540e\u91cd\u8bd5', // 音频约束不满足时的提示。
  unsupported: '\u5f53\u524d\u9875\u9762\u73af\u5883\u4e0d\u652f\u6301\u9ea6\u514b\u98ce\u91c7\u96c6\uff0c\u8bf7\u4f7f\u7528\u6700\u65b0\u7248\u6d4f\u89c8\u5668', // 浏览器不支持采集时的提示。
  insecureContext: '\u5f53\u524d\u9875\u9762\u4e0d\u662f\u5b89\u5168\u4e0a\u4e0b\u6587\uff0c\u624b\u673a\u8bbf\u95ee\u65f6\u8bf7\u4f7f\u7528 HTTPS\uff1b\u672c\u673a\u8c03\u8bd5\u53ef\u4f7f\u7528 localhost', // 非安全上下文时的提示。
  signaling: '\u8fde\u63a5\u4fe1\u4ee4\u670d\u52a1\u5668\u5931\u8d25', // WebSocket 信令连接失败提示。
  server: '\u4fe1\u4ee4\u670d\u52a1\u8fd4\u56de\u9519\u8bef', // 后端返回错误消息时的兜底提示。
} as const

// 房间成员在前端展示时需要的基础状态；当前 UI 暂未消费 users，仅作为后续成员列表/说话状态的预留类型。
interface User {
  id: string // 后端生成的用户唯一标识。
  username: string // 用户进入房间时填写的显示名称。
  isMuted: boolean // 当前用户是否处于静音状态。
  isSpeaking: boolean // 当前用户是否正在说话。
}

// Hook 内部统一维护页面要展示的语音房状态，组件只读取这些状态并渲染 UI。
interface VoiceRoomState {
  localStream: MediaStream | null // 本地麦克风采集到的音频流，null 表示尚未接入。
  remoteStream: MediaStream | null // 对端传来的音频流，null 表示暂无远端音频。
  users: User[] // 房间成员列表预留字段，当前组件没有读取它。
  isConnected: boolean // WebRTC 是否已经成功建立音频连接。
  isMuted: boolean // 本地麦克风是否被静音。
  isSpeakerOn: boolean // 页面扬声器播放开关的 UI 状态。
  error: string | null // 当前要展示给用户的错误提示，null 表示没有错误。
}

// 对外暴露给页面组件的状态和操作方法，隐藏 WebSocket 与 WebRTC 的底层细节。
interface UseVoiceRoomReturn extends VoiceRoomState {
  joinRoom: (roomId: string, username: string) => Promise<boolean> // 加入指定房间并初始化语音连接。
  leaveRoom: () => void // 离开房间并释放麦克风、WebSocket 和 WebRTC 资源。
  toggleMute: () => void // 切换本地麦克风静音状态。
  toggleSpeaker: () => void // 切换扬声器播放开关状态。
}

// 把浏览器抛出的媒体设备错误翻译成用户能理解的提示文案。
function getMicrophoneErrorMessage(error: Error): string {
  // Map browser/media errors to user-facing guidance so HTTPS and permission issues are actionable.
  if (error.name === 'NotAllowedError') {
    return MICROPHONE_ERROR_MESSAGES.denied
  }

  if (error.name === 'NotFoundError' || error.message === 'NO_AUDIO_TRACK') {
    return MICROPHONE_ERROR_MESSAGES.notFound
  }

  if (error.name === 'NotReadableError') {
    return MICROPHONE_ERROR_MESSAGES.inUse
  }

  if (error.name === 'OverconstrainedError') {
    return MICROPHONE_ERROR_MESSAGES.constrained
  }

  if (error.message === 'INSECURE_CONTEXT') {
    return MICROPHONE_ERROR_MESSAGES.insecureContext
  }

  if (error.message === 'MEDIA_DEVICES_UNAVAILABLE') {
    return MICROPHONE_ERROR_MESSAGES.unsupported
  }

  return MICROPHONE_ERROR_MESSAGES.default
}

// useVoiceRoom 负责串起三个环节：采集本地麦克风、通过 WebSocket 交换信令、用 WebRTC 播放远端音频。
export function useVoiceRoom(): UseVoiceRoomReturn {
  // state 会驱动 React 页面刷新，例如显示本地音频、远程音频和连接状态。
  const [state, setState] = useState<VoiceRoomState>({
    localStream: null,
    remoteStream: null,
    users: [], // 当前未被 UI 消费，保留为空数组作为后续成员列表扩展入口。
    isConnected: false,
    isMuted: false,
    isSpeakerOn: true,
    error: null,
  })

  // 这些 ref 保存不会直接触发页面刷新的底层连接对象，避免每次事件回调都拿到过期值。
  const wsRef = useRef<WebSocket | null>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  const localStreamRef = useRef<MediaStream | null>(null)
  const currentRoomIdRef = useRef<string>('')
  const currentUsernameRef = useRef<string>('')

  // 初始化本地麦克风。成功后把 MediaStream 同时放进 ref 和 state：
  // ref 供 WebRTC 添加音轨使用，state 供页面展示“本地音频已连接”。
  const initLocalStream = useCallback(async () => {
    try {
      console.log('Requesting microphone access...')

      const isLocalhost = LOCALHOST_HOSTNAMES.has(window.location.hostname)
      // Microphone capture requires a secure context outside localhost.
      if (!window.isSecureContext && !isLocalhost) {
        throw new Error('INSECURE_CONTEXT')
      }

      if (!navigator.mediaDevices?.getUserMedia) {
        throw new Error('MEDIA_DEVICES_UNAVAILABLE')
      }

      // 这里只采集音频，并启用浏览器内置的回声消除、降噪和自动增益控制。
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        },
        video: false,
      })

      console.log('Microphone ready, tracks:', stream.getAudioTracks().length)

      if (stream.getAudioTracks().length === 0) {
        throw new Error('NO_AUDIO_TRACK')
      }

      localStreamRef.current = stream
      setState(prev => ({ ...prev, localStream: stream, error: null }))
      return stream
    } catch (err) {
      console.error('Failed to get microphone stream:', err)

      const error = err instanceof Error ? err : new Error('UNKNOWN_MEDIA_ERROR')
      setState(prev => ({
        ...prev,
        localStream: null,
        error: getMicrophoneErrorMessage(error),
      }))
      return null
    }
  }, [])

  const createPeerConnection = useCallback(() => {
    console.log('Creating WebRTC peer connection...')
    const pc = new RTCPeerConnection(ICE_SERVERS)

    // 把本地麦克风音轨加入连接，对端收到后才能播放我们的声音。
    if (localStreamRef.current) {
      console.log('Adding local tracks to peer connection...')
      localStreamRef.current.getTracks().forEach(track => {
        pc.addTrack(track, localStreamRef.current!)
      })
    }

    // 对端音轨到达时，浏览器会触发 ontrack；这里把远端流写入 state 交给 audio 标签播放。
    pc.ontrack = event => {
      console.log('Received remote media stream')
      setState(prev => ({ ...prev, remoteStream: event.streams[0] }))
    }

    // ICE candidate 是浏览器发现的可连接地址，需要通过信令服务器转发给房间里的另一端。
    pc.onicecandidate = event => {
      if (event.candidate && wsRef.current) {
        wsRef.current.send(JSON.stringify({
          type: 'ice',
          room_id: currentRoomIdRef.current,
          payload: event.candidate,
        }))
      }
    }

    // WebRTC 真正连通后更新页面状态；当前代码只在 connected 时置为成功。
    pc.onconnectionstatechange = () => {
      console.log('WebRTC connection state:', pc.connectionState)
      if (pc.connectionState === 'connected') {
        setState(prev => ({ ...prev, isConnected: true }))
      }
    }

    pcRef.current = pc
    return pc
  }, [])

  // 处理后端转发来的信令消息。Offer/Answer/ICE 三类消息共同完成 WebRTC 协商。
  const handleSignaling = useCallback(async (data: any) => {
    const pc = pcRef.current
    if (!pc) return

    switch (data.type) {
      case 'waiting':
        console.log('Waiting for another user to join...')
        setState(prev => ({ ...prev, isConnected: false }))
        break

      case 'room_ready': {
        console.log('Room is ready, creating offer...')
        // 后进入房间的一方收到 room_ready 后主动创建 offer，作为本次协商的发起方。
        const offer = await pc.createOffer()
        await pc.setLocalDescription(offer)
        wsRef.current?.send(JSON.stringify({
          type: 'offer',
          room_id: currentRoomIdRef.current,
          payload: offer,
        }))
        break
      }

      case 'offer': {
        console.log('Received offer, creating answer...')
        // 先保存对端 offer，再创建自己的 answer 回传，双方的媒体参数才能对齐。
        await pc.setRemoteDescription(new RTCSessionDescription(data.payload))
        const answer = await pc.createAnswer()
        await pc.setLocalDescription(answer)
        wsRef.current?.send(JSON.stringify({
          type: 'answer',
          room_id: currentRoomIdRef.current,
          payload: answer,
        }))
        break
      }

      case 'answer':
        console.log('Received answer, completing connection...')
        // 发起方收到 answer 后保存远端描述，至此 SDP 协商完成，随后等待 ICE 连通。
        await pc.setRemoteDescription(new RTCSessionDescription(data.payload))
        break

      case 'ice':
        console.log('Received ICE candidate')
        // 收到对端候选地址后交给 RTCPeerConnection，浏览器会自动尝试建立可用链路。
        await pc.addIceCandidate(new RTCIceCandidate(data.payload))
        break

      case 'user_left':
        console.log('Remote user left room')
        // 对方离开后清空远端音频，并重建一个新的 PeerConnection 等待下一位用户加入。
        setState(prev => ({
          ...prev,
          isConnected: false,
          remoteStream: null,
        }))
        pc.close()
        pcRef.current = null
        createPeerConnection()
        break

      case 'error':
        setState(prev => ({
          ...prev,
          error: data?.payload?.message || MICROPHONE_ERROR_MESSAGES.server,
        }))
        break
    }
  }, [createPeerConnection])

  // 加入房间的完整流程：先拿麦克风，再连 WebSocket，最后创建 WebRTC 连接等待信令。
  const joinRoom = useCallback(async (roomId: string, username: string) => {
    currentRoomIdRef.current = roomId
    currentUsernameRef.current = username

    const stream = await initLocalStream()
    if (!stream) return false

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    // 信令服务器 WebSocket 使用当前页面同源地址，开发环境由 Vite 代理，生产环境由 Nginx 转发。
    const wsUrl = `${protocol}//${window.location.host}/ws`

    console.log('Connecting signaling WebSocket:', { wsUrl, roomId, username })
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      console.log('Signaling WebSocket connected:', { wsUrl, roomId, username })
      // 信令 WebSocket 建立后把房间号和用户名发给后端，后端会返回 waiting 或 room_ready。
      ws.send(JSON.stringify({
        type: 'join',
        room_id: roomId,
        payload: username,
      }))
    }

    ws.onmessage = event => {
      console.log('Received signaling WebSocket message:', { wsUrl, data: event.data })
      // 信令服务器消息统一交给 handleSignaling，这样 WebSocket 只负责收包，协商逻辑集中在一处。
      const data = JSON.parse(event.data)
      handleSignaling(data)
    }

    ws.onerror = error => {
      // onerror 不会暴露太多底层原因，因此把信令服务器地址和当前房间一起打出来方便和后端/Nginx 日志对齐。
      console.error('Signaling WebSocket connection failed:', {
        wsUrl,
        roomId: currentRoomIdRef.current,
        readyState: ws.readyState,
        time: new Date().toISOString(),
        error,
      })
      setState(prev => ({ ...prev, error: MICROPHONE_ERROR_MESSAGES.signaling }))
    }

    ws.onclose = event => {
      // 记录浏览器能拿到的关闭细节，排查代理超时或异常断开时重点看 code 和 wasClean。
      console.log('Signaling WebSocket closed:', {
        wsUrl,
        code: event.code,
        reason: event.reason || '(empty)',
        wasClean: event.wasClean,
        readyState: ws.readyState,
        roomId: currentRoomIdRef.current,
        time: new Date().toISOString(),
      })
    }

    createPeerConnection()
    return true
  }, [initLocalStream, handleSignaling, createPeerConnection])

  // 离开房间时释放所有实时通信资源，避免麦克风继续占用或旧连接继续收到事件。
  const leaveRoom = useCallback(() => {
    if (pcRef.current) {
      pcRef.current.close()
      pcRef.current = null
    }

    if (localStreamRef.current) {
      localStreamRef.current.getTracks().forEach(track => track.stop())
      localStreamRef.current = null
    }

    if (wsRef.current) {
      wsRef.current.close()
      wsRef.current = null
    }

    setState({
      localStream: null,
      remoteStream: null,
      users: [], // 重置预留成员列表；当前页面不会读取该字段。
      isConnected: false,
      isMuted: false,
      isSpeakerOn: true,
      error: null,
    })
  }, [])

  // 静音不是停止麦克风，而是临时禁用本地音轨；再次点击可以恢复发送声音。
  const toggleMute = useCallback(() => {
    if (localStreamRef.current) {
      const audioTrack = localStreamRef.current.getAudioTracks()[0]
      if (audioTrack) {
        audioTrack.enabled = state.isMuted
        setState(prev => ({ ...prev, isMuted: !prev.isMuted }))
      }
    }
  }, [state.isMuted])

  // 扬声器开关目前只更新 UI 状态，真正控制远端 audio 播放可在 Room 组件里继续接入。
  const toggleSpeaker = useCallback(() => {
    setState(prev => ({ ...prev, isSpeakerOn: !prev.isSpeakerOn }))
  }, [])

  // 组件卸载时自动离开房间，防止用户切换页面后连接和麦克风仍然挂着。
  useEffect(() => {
    return () => {
      leaveRoom()
    }
  }, [leaveRoom])

  return {
    ...state,
    joinRoom,
    leaveRoom,
    toggleMute,
    toggleSpeaker,
  }
}
