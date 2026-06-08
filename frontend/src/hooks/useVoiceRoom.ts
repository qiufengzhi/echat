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

const ICE_SERVERS: RTCConfiguration = {
  iceServers: [
    { urls: 'stun:stun.l.google.com:19302' },
    { urls: 'stun:stun1.l.google.com:19302' },
  ],
}

const LOCALHOST_HOSTNAMES = new Set(['localhost', '127.0.0.1', '::1'])

const MICROPHONE_ERROR_MESSAGES = {
  default: '无法访问麦克风，请检查浏览器权限和设备状态',
  denied: '麦克风权限被拒绝，请在浏览器地址栏中允许麦克风访问',
  notFound: '没有检测到可用麦克风，请检查设备是否已连接',
  inUse: '麦克风可能被其他应用占用，请关闭占用后重试',
  constrained: '当前音频设备不满足采集条件，请更换麦克风后重试',
  unsupported: '当前页面环境不支持麦克风采集，请使用最新版浏览器',
  insecureContext: '当前页面暂时无法使用麦克风，请检查浏览器访问环境和麦克风权限',
  signaling: '房间连接失败，请稍后重试',
  server: '房间服务返回错误',
} as const

export interface User {
  id: string
  username: string
  isMuted: boolean
  isSpeaking: boolean
}

interface VoiceRoomState {
  localStream: MediaStream | null
  remoteStream: MediaStream | null
  users: User[]
  hostId: string | null
  isHost: boolean
  isConnected: boolean
  isMuted: boolean
  isSpeakerOn: boolean
  error: string | null
}

interface UseVoiceRoomReturn extends VoiceRoomState {
  joinRoom: (roomId: string, username: string) => Promise<boolean>
  leaveRoom: (nextHostId?: string) => void
  toggleMute: () => void
  toggleSpeaker: () => void
}

function createEmptyVoiceRoomState(): VoiceRoomState {
  return {
    localStream: null,
    remoteStream: null,
    users: [],
    hostId: null,
    isHost: false,
    isConnected: false,
    isMuted: false,
    isSpeakerOn: true,
    error: null,
  }
}

function getMicrophoneErrorMessage(error: Error): string {
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

function createRoomUser(id: string, username: string, isMuted = false): User {
  return {
    id,
    username,
    isMuted,
    isSpeaking: false,
  }
}

function upsertRoomUser(users: User[], nextUser: User): User[] {
  const existingIndex = users.findIndex(user => user.id === nextUser.id)
  if (existingIndex === -1) return [...users, nextUser]

  return users.map(user => (user.id === nextUser.id ? { ...user, ...nextUser } : user))
}

export function useVoiceRoom(): UseVoiceRoomReturn {
  const [state, setState] = useState<VoiceRoomState>(createEmptyVoiceRoomState)

  const signalingClientRef = useRef<SignalingClient | null>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  const localStreamRef = useRef<MediaStream | null>(null)
  const currentUserIdRef = useRef<string | null>(null)
  const currentUsernameRef = useRef('')

  const syncHostState = useCallback((hostId: string | null) => {
    setState(prev => ({
      ...prev,
      hostId,
      isHost: Boolean(hostId && currentUserIdRef.current && hostId === currentUserIdRef.current),
    }))
  }, [])

  const initLocalStream = useCallback(async () => {
    try {
      console.log('Requesting microphone access...')

      const isLocalhost = LOCALHOST_HOSTNAMES.has(window.location.hostname)
      if (!window.isSecureContext && !isLocalhost) {
        throw new Error('INSECURE_CONTEXT')
      }

      if (!navigator.mediaDevices?.getUserMedia) {
        throw new Error('MEDIA_DEVICES_UNAVAILABLE')
      }

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

    if (localStreamRef.current) {
      console.log('Adding local tracks to peer connection...')
      localStreamRef.current.getTracks().forEach(track => {
        pc.addTrack(track, localStreamRef.current!)
      })
    }

    pc.ontrack = event => {
      console.log('Received remote media stream')
      setState(prev => ({ ...prev, remoteStream: event.streams[0] }))
    }

    pc.onicecandidate = event => {
      if (event.candidate) {
        signalingClientRef.current?.sendIce(event.candidate)
      }
    }

    pc.onconnectionstatechange = () => {
      console.log('WebRTC connection state:', pc.connectionState)
      if (pc.connectionState === 'connected') {
        setState(prev => ({ ...prev, isConnected: true }))
      }
    }

    pcRef.current = pc
    return pc
  }, [])

  const syncUsersFromSignaling = useCallback((data: SignalingMessage) => {
    if (data.user_id && (data.type === 'waiting' || data.type === 'room_ready')) {
      currentUserIdRef.current = data.user_id
    }

    if (data.type === 'waiting' && data.user_id) {
      const payload = data.payload as WaitingPayload | undefined
      const currentUser = createRoomUser(data.user_id, currentUsernameRef.current, state.isMuted)
      setState(prev => ({
        ...prev,
        users: upsertRoomUser(prev.users, currentUser),
        hostId: payload?.host_id || prev.hostId,
        isHost: Boolean((payload?.host_id || prev.hostId) && (payload?.host_id || prev.hostId) === data.user_id),
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
        users: prev.users.filter(user => user.id !== payload.user_id),
        hostId: payload.host_id || prev.hostId,
        isHost: Boolean((payload.host_id || prev.hostId) && (payload.host_id || prev.hostId) === currentUserIdRef.current),
      }))
      return
    }

    if (data.type === 'host_changed') {
      const payload = data.payload as HostChangedPayload | undefined
      if (!payload?.host_id) return

      syncHostState(payload.host_id)
    }
  }, [state.isMuted, syncHostState])

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

  const joinRoom = useCallback(async (roomId: string, username: string) => {
    currentUsernameRef.current = username

    const stream = await initLocalStream()
    if (!stream) return false

    setState(prev => ({
      ...prev,
      users: [],
      hostId: null,
      isHost: false,
      error: null,
    }))

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

    setState(createEmptyVoiceRoomState())
  }, [])

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

  const toggleSpeaker = useCallback(() => {
    setState(prev => ({ ...prev, isSpeakerOn: !prev.isSpeakerOn }))
  }, [])

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

      currentUserIdRef.current = null
      currentUsernameRef.current = ''
    }
  }, [])

  return {
    ...state,
    joinRoom,
    leaveRoom,
    toggleMute,
    toggleSpeaker,
  }
}
