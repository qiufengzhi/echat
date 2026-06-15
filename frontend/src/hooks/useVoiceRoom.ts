import { useState, useEffect, useRef, useCallback } from 'react'

import { SignalingClient } from '../services/signalingClient'
import { handleWebRTCSignaling } from '../services/webrtcSignalingHandler'
import type {
  HostChangedPayload,
  RoomReadyPayload,
  SignalingMessage,
  UserJoinedPayload,
  UserLeftPayload,
  WaitingPayload,
} from '../types/signaling'

// 本 Hook 是前端语音房的实时通信适配层。
// SFU 架构下，前端只与 SFU 服务端建立一条 PeerConnection：
//   - 本地音轨加入该连接
//   - 服务端转发其他成员的音轨过来，通过 ontrack 按用户分组存放
//   - 成员列表和房主状态仍由信令消息同步

// WebRTC 需要借助 STUN 服务器发现可用于直连的候选地址。
const ICE_SERVERS: RTCConfiguration = {
  iceServers: [
    { urls: 'stun:stun.l.google.com:19302' }, // 主 STUN 地址。
    { urls: 'stun:stun1.l.google.com:19302' }, // 备用 STUN 地址。
  ],
}

// 浏览器要求麦克风采集运行在安全上下文；localhost 是本地开发时的例外。
const LOCALHOST_HOSTNAMES = new Set(['localhost', '127.0.0.1', '::1'])

// MICROPHONE_ERROR_MESSAGES 报错文案
const MICROPHONE_ERROR_MESSAGES = {
  default: '无法访问麦克风，请检查浏览器权限和设备状态', // 通用麦克风错误提示。
  denied: '麦克风权限被拒绝，请在浏览器地址栏中允许麦克风访问', // 用户拒绝授权时的提示。
  notFound: '没有检测到可用麦克风，请检查设备是否已连接', // 未检测到麦克风时的提示。
  inUse: '麦克风可能被其他应用占用，请关闭占用后重试', // 麦克风被占用时的提示。
  constrained: '当前音频设备不满足采集条件，请更换麦克风后重试', // 音频约束不满足时的提示。
  unsupported: '当前页面环境不支持麦克风采集，请使用最新版浏览器', // 浏览器不支持采集时的提示。
  insecureContext: '当前页面暂时无法使用麦克风，请检查浏览器访问环境和麦克风权限', // 非安全上下文时的用户提示。
  signaling: '房间连接失败，请稍后重试', // 房间连接失败时给用户看的提示。
  server: '房间服务返回错误', // 后端返回错误消息时的兜底提示。
} as const

// 音频设备类型，用于 SettingsModal 中的设备选择下拉框。
export interface AudioDevice {
  deviceId: string
  label: string
}

// 房间成员在前端展示时需要的基础状态，页面会用它渲染成员席位和成员列表。
export interface User {
  id: string // 后端生成的用户唯一标识。
  username: string // 用户进入房间时填写的显示名称。
  isMuted: boolean // 当前成员是否处于静音状态。
  isSpeaking: boolean // 当前成员是否正在说话，后续可接入音量检测。
}

// Hook 内部统一维护页面要展示的语音房状态，组件只读取这些状态并渲染 UI。
interface VoiceRoomState {
  localStream: MediaStream | null // 本地麦克风采集到的音频流，null 表示尚未接入。
  remoteStreams: Map<string, MediaStream> // SFU 架构下多个远端用户的音频流，key 为用户 ID。
  users: User[] // 房间成员列表，由信令消息同步，供多人席位和成员列表展示。
  hostId: string | null // 当前房主 ID，来自服务端权威状态。
  isHost: boolean // 当前用户是否为房主，用于控制离开时是否展示交接弹窗。
  isConnected: boolean // WebRTC 是否已经成功建立音频连接。
  isMuted: boolean // 本地麦克风是否被静音。
  isSpeakerOn: boolean // 页面扬声器播放开关的 UI 状态。
  error: string | null // 当前要展示给用户的错误提示，null 表示没有错误。
  availableMicrophones: AudioDevice[] // 可用的麦克风设备列表。
  availableSpeakers: AudioDevice[] // 可用的扬声器设备列表。
  currentMicrophoneId: string | null // 当前选中的麦克风设备 ID。
}

// 对外暴露给页面组件的状态和操作方法，隐藏 WebSocket 与 WebRTC 的底层细节。
interface UseVoiceRoomReturn extends VoiceRoomState {
  joinRoom: (roomId: string, username: string) => Promise<boolean> // 加入指定房间并初始化语音连接。
  leaveRoom: (nextHostId?: string) => void // 离开房间并释放资源，房主可传下一任房主 ID。
  toggleMute: () => void // 切换本地麦克风静音状态。
  toggleSpeaker: () => void // 切换扬声器播放开关状态。
  refreshAudioDevices: () => Promise<void> // 刷新可用音频设备列表，供 SettingsModal 设备下拉框使用。
  switchMicrophone: (deviceId: string) => Promise<void> // 切换到指定麦克风设备。
}

// createEmptyVoiceRoomState 返回初始状态，也用于离开房间后重置页面。
function createEmptyVoiceRoomState(): VoiceRoomState {
  return {
    localStream: null,
    remoteStreams: new Map(),
    users: [],
    hostId: null,
    isHost: false,
    isConnected: false,
    isMuted: false,
    isSpeakerOn: true,
    error: null,
    availableMicrophones: [],
    availableSpeakers: [],
    currentMicrophoneId: null,
  }
}

// 把浏览器抛出的媒体设备错误翻译成用户能理解的提示文案。
function getMicrophoneErrorMessage(error: Error): string {
  // 这里按浏览器错误类型拆分，方便用户知道是权限、设备、占用还是访问环境问题。
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
    // 当前版本还没有接入音量检测，后续可以由 Web Audio 或后端成员状态更新。
    isSpeaking: false,
  }
}

// upsertRoomUser 按 ID 合并成员，避免同一个成员重复出现在列表里。
function upsertRoomUser(users: User[], nextUser: User): User[] {
  // 后端 user_id 是成员列表的稳定主键，比昵称更可靠；昵称可能重复或后续允许修改。
  const existingIndex = users.findIndex(user => user.id === nextUser.id)
  if (existingIndex === -1) return [...users, nextUser]

  return users.map(user => (user.id === nextUser.id ? { ...user, ...nextUser } : user))
}

// useVoiceRoom 负责串起三个环节：
//   1. 采集本地麦克风
//   2. 通过 WebSocket 交换 SFU 信令（sfu_offer / sfu_answer / sfu_ice）
//   3. 与 SFU 服务端建立单条 PeerConnection，接收多路远端音频流
export function useVoiceRoom(): UseVoiceRoomReturn {
  // state 会驱动 React 页面刷新，例如成员席位、房主标记、远程音频和连接状态。
  const [state, setState] = useState<VoiceRoomState>(createEmptyVoiceRoomState)

  // 这些 ref 保存不会直接触发页面刷新的底层连接对象，避免事件回调拿到过期值。
  const signalingClientRef = useRef<SignalingClient | null>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  const localStreamRef = useRef<MediaStream | null>(null)
  // 当前用户 ID 由服务端在 waiting/room_ready 中返回，用来判断自己是不是房主。
  const currentUserIdRef = useRef<string | null>(null)
  // 当前昵称用于加入成功前的本地兜底展示，也用于同步自己的静音状态。
  const currentUsernameRef = useRef('')

  // syncHostState 只同步房主相关状态，供 host_changed 和成员事件复用。
  const syncHostState = useCallback((hostId: string | null) => {
    setState(prev => ({
      ...prev,
      hostId,
      isHost: Boolean(hostId && currentUserIdRef.current && hostId === currentUserIdRef.current),
    }))
  }, [])

  // 初始化本地麦克风。成功后把 MediaStream 同时放进 ref 和 state：
  // ref 供 WebRTC 添加音轨使用，state 供页面展示"本地音频已连接"。
  // deviceId 可选，不传时使用浏览器默认麦克风。
  const initLocalStream = useCallback(async (deviceId?: string) => {
    try {
      console.log('正在请求麦克风权限...', deviceId ? `deviceId: ${deviceId}` : '(默认)')

      const isLocalhost = LOCALHOST_HOSTNAMES.has(window.location.hostname)
      // 非 localhost 页面必须是安全上下文，否则浏览器会阻止麦克风采集。
      if (!window.isSecureContext && !isLocalhost) {
        throw new Error('INSECURE_CONTEXT')
      }

      if (!navigator.mediaDevices?.getUserMedia) {
        throw new Error('MEDIA_DEVICES_UNAVAILABLE')
      }

      // 音频配置：启用回声消除、降噪和自动增益控制。
      // 如果指定了 deviceId，则使用指定设备；否则使用默认设备。
      const audioConfig: MediaStreamConstraints['audio'] = deviceId
        ? { deviceId: { exact: deviceId }, echoCancellation: true, noiseSuppression: true, autoGainControl: true }
        : { echoCancellation: true, noiseSuppression: true, autoGainControl: true }

      // 这里只采集音频。
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: audioConfig,
        video: false,
      })

      console.log('麦克风已就绪, tracks:', stream.getAudioTracks().length)

      if (stream.getAudioTracks().length === 0) {
        throw new Error('NO_AUDIO_TRACK')
      }

      localStreamRef.current = stream
      setState(prev => ({
        ...prev,
        localStream: stream,
        error: null,
        currentMicrophoneId: deviceId || null,
      }))
      return stream
    } catch (err) {
      console.error('获取麦克风流失败:', err)

      const error = err instanceof Error ? err : new Error('UNKNOWN_MEDIA_ERROR')
      setState(prev => ({
        ...prev,
        localStream: null,
        error: getMicrophoneErrorMessage(error),
      }))
      return null
    }
  }, [])

  // refreshAudioDevices 枚举当前可用的音频输入/输出设备，供 SettingsModal 设备下拉框展示。
  // 设备标签可能为空（浏览器隐私策略限制），此时用"麦克风 1"、"麦克风 2"等序号兜底。
  const refreshAudioDevices = useCallback(async () => {
    try {
      const devices = await navigator.mediaDevices.enumerateDevices()
      const microphones = devices
        .filter(device => device.kind === 'audioinput')
        .map((device, index) => ({
          deviceId: device.deviceId,
          label: device.label || `麦克风 ${index + 1}`,
        }))
      const speakers = devices
        .filter(device => device.kind === 'audiooutput')
        .map((device, index) => ({
          deviceId: device.deviceId,
          label: device.label || `扬声器 ${index + 1}`,
        }))

      setState(prev => ({
        ...prev,
        availableMicrophones: microphones,
        availableSpeakers: speakers,
      }))
    } catch (err) {
      console.error('枚举设备失败:', err)
    }
  }, [])

  // switchMicrophone 停止当前麦克风并切换到指定设备，同时更新 PeerConnection 的音轨。
  const switchMicrophone = useCallback(
    async (deviceId: string) => {
      // 先停止旧音轨。
      if (localStreamRef.current) {
        localStreamRef.current.getTracks().forEach(track => track.stop())
      }

      // 请求新设备。
      const stream = await initLocalStream(deviceId)
      if (!stream) return

      // 如果存在活跃的 WebRTC 连接，需要用新音轨替换旧音轨。
      if (pcRef.current) {
        const senders = pcRef.current.getSenders()
        const audioSender = senders.find(sender => sender.track?.kind === 'audio')
        if (audioSender) {
          await audioSender.replaceTrack(stream.getAudioTracks()[0])
        }
      }

      console.log('已切换麦克风设备:', deviceId)
    },
    [initLocalStream],
  )

  // createPeerConnection 创建与 SFU 服务端的单条 PeerConnection，加入本地音轨，
  // 并绑定 ontrack（接收多路远端音频）、onicecandidate（转发 ICE 给 SFU）和连接状态事件。
  const createPeerConnection = useCallback(() => {
    console.log('[sfu] 正在创建 SFU PeerConnection...')
    const pc = new RTCPeerConnection(ICE_SERVERS)

    // 把本地麦克风音轨加入连接，SFU 服务端收到后转发给房间内其他成员。
    if (localStreamRef.current) {
      console.log('[sfu] 正在将本地音轨添加到 SFU PeerConnection...')
      localStreamRef.current.getTracks().forEach(track => {
        pc.addTrack(track, localStreamRef.current!)
      })
    }

    // ontrack 处理 SFU 服务端转发来的各路远端音频。
    // SFU 服务端为每个远端用户创建一个音轨，label 格式为 "audio_<userId>"。
    // 前端根据 label 提取 userId，将 track 归入该用户的 MediaStream。
    pc.ontrack = event => {
      console.log('[sfu] 收到远端音轨:', event.track.label)
      // 从 track label 中提取源用户 ID，格式为 "audio_<userId>"。
      const sourceUserId = event.track.label.replace(/^audio_/, '')
      if (!sourceUserId) {
        console.warn('[sfu] 收到无法识别来源的音轨:', event.track.label)
        return
      }

      setState(prev => {
        const newStreams = new Map(prev.remoteStreams)
        let userStream = newStreams.get(sourceUserId)

        if (!userStream) {
          // 该用户的首条音轨，创建新的 MediaStream。
          userStream = new MediaStream([event.track])
          newStreams.set(sourceUserId, userStream)
          console.log('[sfu] 为用户创建新音频流:', sourceUserId)
        } else {
          // 已有该用户的流，添加新音轨（通常 SFU 每用户只发一条 audio track）。
          // 先移除旧音轨再添加新音轨，避免多个同类型音轨冲突。
          userStream.getAudioTracks().forEach(t => userStream!.removeTrack(t))
          userStream.addTrack(event.track)
          console.log('[sfu] 已替换用户的音轨:', sourceUserId)
        }

        return { ...prev, remoteStreams: newStreams }
      })
    }

    // ICE candidate 收集后发给 SFU 服务端，由 SFU 统一管理 ICE 交换。
    pc.onicecandidate = event => {
      if (event.candidate) {
        signalingClientRef.current?.sendSFUIce(event.candidate)
      }
    }

    // WebRTC 连通后更新页面状态。
    pc.onconnectionstatechange = () => {
      console.log('[sfu] SFU 连接状态:', pc.connectionState)
      if (pc.connectionState === 'connected') {
        setState(prev => ({ ...prev, isConnected: true }))
      }
      // 连接断开/失败时，通过 signaling handler 兜底处理。
    }

    pcRef.current = pc
    return pc
  }, [])

  // syncUsersFromSignaling 根据信令消息同步成员列表和房主状态，不处理 Offer/Answer/ICE 协商。
  const syncUsersFromSignaling = useCallback((data: SignalingMessage) => {
    // waiting 和 room_ready 都会携带服务端分配给当前连接的 user_id。
    if (data.user_id && (data.type === 'waiting' || data.type === 'room_ready')) {
      currentUserIdRef.current = data.user_id
    }

    if (data.type === 'waiting' && data.user_id) {
      const payload = data.payload as WaitingPayload | undefined
      // 房间只有自己时，也要先把自己放进成员列表，页面才能展示房主席位。
      setState(prev => {
        const currentUser = createRoomUser(data.user_id!, currentUsernameRef.current, prev.isMuted)
        return {
          ...prev,
          users: upsertRoomUser(prev.users, currentUser),
          hostId: payload?.host_id || prev.hostId,
          isHost: Boolean((payload?.host_id || prev.hostId) && (payload?.host_id || prev.hostId) === data.user_id),
        }
      })
      return
    }

    if (data.type === 'room_ready') {
      const payload = data.payload as RoomReadyPayload | undefined
      if (!payload?.users) return

      setState(prev => ({
        ...prev,
        // 以服务端快照为准重建成员列表，避免本地等待态或重复广播造成成员残留。
        users: payload.users.map(user =>
          createRoomUser(user.id, user.username, user.id === currentUserIdRef.current ? prev.isMuted : false),
        ),
        hostId: payload.host_id || prev.hostId,
        isHost: Boolean(payload.host_id && payload.host_id === currentUserIdRef.current),
      }))
      return
    }

    if (data.type === 'user_joined') {
      const payload = data.payload as UserJoinedPayload | undefined
      if (!payload?.user_id || !payload.username) return

      setState(prev => ({
        ...prev,
        // 新成员加入只增量合并，保留本地已有成员的静音/说话展示状态。
        users: upsertRoomUser(prev.users, createRoomUser(payload.user_id, payload.username)),
        hostId: payload.host_id || prev.hostId,
        isHost: Boolean((payload.host_id || prev.hostId) && (payload.host_id || prev.hostId) === currentUserIdRef.current),
      }))
      return
    }

    if (data.type === 'user_left') {
      const payload = data.payload as UserLeftPayload | undefined
      if (!payload?.user_id) return

      setState(prev => ({
        ...prev,
        // 成员离开后立刻从 UI 列表移除；host_id 会同步房主交接后的最终状态。
        users: prev.users.filter(user => user.id !== payload.user_id),
        hostId: payload.host_id || prev.hostId,
        isHost: Boolean((payload.host_id || prev.hostId) && (payload.host_id || prev.hostId) === currentUserIdRef.current),
      }))
      return
    }

    if (data.type === 'host_changed') {
      const payload = data.payload as HostChangedPayload | undefined
      if (!payload?.host_id) return

      // host_changed 是显式房主变更事件，即使成员列表没变化，也要刷新房主权限。
      syncHostState(payload.host_id)
    }
  }, [syncHostState])

  // 处理 SFU 信令消息。成员/房主状态先同步，再处理信令协商消息。
  // 客户端发起 Offer：收到 waiting/room_ready 后创建 Offer 并发送。
  const handleSignaling = useCallback(
    async (data: SignalingMessage) => {
      syncUsersFromSignaling(data)

      // 收到 waiting 或 room_ready 后，客户端创建 SDP Offer 并发给服务端。
      if (data.type === 'waiting' || data.type === 'room_ready') {
        const pc = pcRef.current
        if (!pc) {
          console.warn('[sfu] 没有 PeerConnection 可用来创建 Offer')
          return
        }
        console.log('[sfu] 收到', data.type, '后正在创建 Offer...')
        const offer = await pc.createOffer()
        await pc.setLocalDescription(offer)
        signalingClientRef.current?.sendSFUOffer(offer)
        return
      }

      await handleWebRTCSignaling(data, {
        getPeerConnection: () => pcRef.current,
        setRemoteStream: (userId, stream) => {
          setState(prev => {
            const newStreams = new Map(prev.remoteStreams)
            newStreams.set(userId, stream)
            return { ...prev, remoteStreams: newStreams }
          })
        },
        removeRemoteStream: userId => {
          setState(prev => {
            const newStreams = new Map(prev.remoteStreams)
            newStreams.delete(userId)
            return { ...prev, remoteStreams: newStreams }
          })
        },
        sendSFUOffer: offer => signalingClientRef.current?.sendSFUOffer(offer),
        sendSFUIce: candidate => signalingClientRef.current?.sendSFUIce(candidate),
        setConnected: connected => setState(prev => ({ ...prev, isConnected: connected })),
        setError: message => setState(prev => ({ ...prev, error: message })),
        serverErrorMessage: MICROPHONE_ERROR_MESSAGES.server,
      })
    },
    [syncUsersFromSignaling],
  )

  // 加入房间的完整流程：先拿麦克风，再连 WebSocket 和创建 PeerConnection。
  // 客户端加入后收到 waiting/room_ready，触发客户端创建 SDP Offer 并发起协商。
  const joinRoom = useCallback(
    async (roomId: string, username: string) => {
      currentUsernameRef.current = username

      const stream = await initLocalStream()
      if (!stream) return false

      // 加入房间时刷新设备列表，供 SettingsModal 展示下拉选项。
      await refreshAudioDevices()

      setState(prev => ({
        ...prev,
        users: [],
        hostId: null,
        isHost: false,
        error: null,
        remoteStreams: new Map(),
      }))

      // 先创建与 SFU 服务端的 PeerConnection（加入本地音轨），
      // 再连接信令。客户端加入后收到 waiting/room_ready 时创建并发送 Offer。
      createPeerConnection()

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

      return true
    },
    [initLocalStream, handleSignaling, createPeerConnection, refreshAudioDevices],
  )

  // 离开房间时先向服务端发送 leave，再释放本地麦克风、WebSocket 和 WebRTC 资源。
  // nextHostId 只在当前用户是房主时有意义；服务端仍会校验它是否在线。
  const leaveRoom = useCallback((nextHostId?: string) => {
    if (signalingClientRef.current) {
      signalingClientRef.current.sendLeave(nextHostId ? { next_host_id: nextHostId } : undefined)
    }

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

    // 彻底回到初始状态，避免下一次进入房间时沿用旧的 hostId 或成员列表。
    setState(createEmptyVoiceRoomState())
  }, [])

  // 静音不是停止麦克风，而是临时禁用本地音轨；再次点击可以恢复发送声音。
  const toggleMute = useCallback(() => {
    if (localStreamRef.current) {
      const audioTrack = localStreamRef.current.getAudioTracks()[0]
      if (audioTrack) {
        audioTrack.enabled = !audioTrack.enabled
        setState(prev => ({ ...prev, isMuted: !audioTrack.enabled }))
      }
    }
  }, [])

  // 切换扬声器播放开关；只是改变了 state.isSpeakerOn 的值，实际的 audio 元素由 RemoteAudio 组件响应。
  const toggleSpeaker = useCallback(() => {
    setState(prev => ({ ...prev, isSpeakerOn: !prev.isSpeakerOn }))
  }, [])

  // 页面关闭或路由离开时释放所有资源。
  useEffect(() => {
    return () => {
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
    }
  }, [])

  return {
    ...state,
    joinRoom,
    leaveRoom,
    toggleMute,
    toggleSpeaker,
    refreshAudioDevices,
    switchMicrophone,
  }
}
