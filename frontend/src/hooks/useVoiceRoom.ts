import { useState, useEffect, useRef, useCallback } from 'react'

import { SignalingClient } from '../services/signalingClient'
import { handleWebRTCSignaling } from '../services/webrtcSignalingHandler'
import type { RoomReadyPayload, SignalingMessage, UserJoinedPayload, UserLeftPayload } from '../types/signaling'

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
  insecureContext: '当前页面暂时无法使用麦克风，请检查浏览器访问环境和麦克风权限', // 非安全上下文时的用户提示。
  signaling: '房间连接失败，请稍后重试', // 房间连接失败时给用户看的提示。
  server: '房间服务返回错误', // 后端返回错误消息时的兜底提示。
} as const

// 房间成员在前端展示时需要的基础状态，页面会用它渲染成员席位和成员列表。
export interface User {
  id: string // 后端生成的用户唯一标识。
  username: string // 用户进入房间时填写的显示名称。
  isMuted: boolean // 当前用户是否处于静音状态。
  isSpeaking: boolean // 当前用户是否正在说话。
}

// Hook 内部统一维护页面要展示的语音房状态，组件只读取这些状态并渲染 UI。
interface VoiceRoomState {
  localStream: MediaStream | null // 本地麦克风采集到的音频流，null 表示尚未接入。
  remoteStream: MediaStream | null // 对端传来的音频流，null 表示暂无远端音频。
  users: User[] // 房间成员列表，由信令消息同步，供多人席位和成员列表展示。
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

// createRoomUser 把后端成员摘要转换成页面需要的成员状态。
function createRoomUser(id: string, username: string, isMuted = false): User {
  return {
    id,
    username,
    isMuted,
    isSpeaking: false,
  }
}

// upsertRoomUser 按 ID 合并成员，避免同一个成员重复出现在列表里。
function upsertRoomUser(users: User[], nextUser: User): User[] {
  const existingIndex = users.findIndex(user => user.id === nextUser.id)
  if (existingIndex === -1) return [...users, nextUser]

  return users.map(user => (user.id === nextUser.id ? { ...user, ...nextUser } : user))
}

// useVoiceRoom 负责串起三个环节：采集本地麦克风、通过 WebSocket 交换信令、用 WebRTC 播放远端音频。
export function useVoiceRoom(): UseVoiceRoomReturn {
  // state 会驱动 React 页面刷新，例如显示本地音频、远程音频和连接状态。
  const [state, setState] = useState<VoiceRoomState>({
    localStream: null,
    remoteStream: null,
    users: [], // 成员列表会在收到信令消息后逐步同步。
    isConnected: false,
    isMuted: false,
    isSpeakerOn: true,
    error: null,
  })

  // 这些 ref 保存不会直接触发页面刷新的底层连接对象，避免每次事件回调都拿到过期值。
  const signalingClientRef = useRef<SignalingClient | null>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  const localStreamRef = useRef<MediaStream | null>(null)
  const currentUserIdRef = useRef<string | null>(null)
  const currentUsernameRef = useRef('')

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
      if (event.candidate) {
        signalingClientRef.current?.sendIce(event.candidate)
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

  // syncUsersFromSignaling 只同步页面成员列表，不处理 Offer/Answer/ICE 协商。
  const syncUsersFromSignaling = useCallback((data: SignalingMessage) => {
    if (data.user_id && (data.type === 'waiting' || data.type === 'room_ready')) {
      currentUserIdRef.current = data.user_id
    }

    if (data.type === 'waiting' && data.user_id) {
      const currentUser = createRoomUser(data.user_id, currentUsernameRef.current, state.isMuted)
      setState(prev => ({
        ...prev,
        users: upsertRoomUser(prev.users, currentUser),
      }))
      return
    }

    if (data.type === 'room_ready') {
      const payload = data.payload as RoomReadyPayload | undefined
      if (!payload?.users) return

      setState(prev => ({
        ...prev,
        users: payload.users.map(user =>
          createRoomUser(user.id, user.username, user.id === currentUserIdRef.current ? prev.isMuted : false),
        ),
      }))
      return
    }

    if (data.type === 'user_joined') {
      const payload = data.payload as UserJoinedPayload | undefined
      if (!payload?.user_id || !payload.username) return

      setState(prev => ({
        ...prev,
        users: upsertRoomUser(prev.users, createRoomUser(payload.user_id, payload.username)),
      }))
      return
    }

    if (data.type === 'user_left') {
      const payload = data.payload as UserLeftPayload | undefined
      if (!payload?.user_id) return

      setState(prev => ({
        ...prev,
        users: prev.users.filter(user => user.id !== payload.user_id),
      }))
    }
  }, [state.isMuted])

  // 处理后端转发来的信令消息。Offer/Answer/ICE 三类消息共同完成 WebRTC 协商。
  const handleSignaling = useCallback(async (data: SignalingMessage) => {
    syncUsersFromSignaling(data)

    await handleWebRTCSignaling(data, {
      getPeerConnection: () => pcRef.current,
      clearPeerConnection: () => {
        pcRef.current = null
      },
      createPeerConnection,
      sendOffer: offer => signalingClientRef.current?.sendOffer(offer),
      sendAnswer: answer => signalingClientRef.current?.sendAnswer(answer),
      setConnected: connected => setState(prev => ({ ...prev, isConnected: connected })),
      clearRemoteStream: () => setState(prev => ({ ...prev, remoteStream: null })),
      setError: message => setState(prev => ({ ...prev, error: message })),
      serverErrorMessage: MICROPHONE_ERROR_MESSAGES.server,
    })
  }, [createPeerConnection, syncUsersFromSignaling])

  // 加入房间的完整流程：先拿麦克风，再连 WebSocket，最后创建 WebRTC 连接等待信令。
  const joinRoom = useCallback(async (roomId: string, username: string) => {
    currentUsernameRef.current = username

    const stream = await initLocalStream()
    if (!stream) return false

    setState(prev => ({
      ...prev,
      // 成员列表只保存后端确认过的真实成员；确认前的“我”由房间页兜底展示。
      users: [],
      error: null,
    }))

    // SignalingClient 只负责连接信令服务器，收到的信令再交回 hook 驱动 WebRTC 协商。
    const signalingClient = new SignalingClient({
      roomId,
      username,
      handlers: {
        onOpen: () => {
          signalingClient.sendJoin()
        },
        onMessage: message => {
          void handleSignaling(message)
        },
        onError: () => {
          setState(prev => ({ ...prev, error: MICROPHONE_ERROR_MESSAGES.signaling }))
        },
      },
    })
    signalingClientRef.current = signalingClient
    signalingClient.connect()

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

    if (signalingClientRef.current) {
      signalingClientRef.current.close()
      signalingClientRef.current = null
    }

    currentUserIdRef.current = null
    currentUsernameRef.current = ''

    setState({
      localStream: null,
      remoteStream: null,
      users: [], // 离开后清空成员列表，避免旧成员残留到下一次进入。
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
        setState(prev => {
          const nextMuted = !prev.isMuted
          return {
            ...prev,
            isMuted: nextMuted,
            users: prev.users.map(user => {
              const isCurrentUser = user.id === currentUserIdRef.current || user.username === currentUsernameRef.current
              return isCurrentUser ? { ...user, isMuted: nextMuted } : user
            }),
          }
        })
      }
    }
  }, [state.isMuted])

  // 扬声器开关控制页面是否播放远端 audio，不影响自己是否把声音发给别人。
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
