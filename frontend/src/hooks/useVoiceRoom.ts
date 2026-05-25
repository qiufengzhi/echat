import { useState, useEffect, useRef, useCallback } from 'react'

const ICE_SERVERS: RTCConfiguration = {
  iceServers: [
    { urls: 'stun:stun.l.google.com:19302' },
    { urls: 'stun:stun1.l.google.com:19302' },
  ]
}

// Browsers treat localhost as a secure context exception during local development.
const LOCALHOST_HOSTNAMES = new Set(['localhost', '127.0.0.1', '::1'])

const MICROPHONE_ERROR_MESSAGES = {
  default: '\u65e0\u6cd5\u8bbf\u95ee\u9ea6\u514b\u98ce\uff0c\u8bf7\u68c0\u67e5\u6d4f\u89c8\u5668\u6743\u9650\u548c\u8bbe\u5907\u72b6\u6001',
  denied: '\u9ea6\u514b\u98ce\u6743\u9650\u88ab\u62d2\u7edd\uff0c\u8bf7\u5728\u6d4f\u89c8\u5668\u5730\u5740\u680f\u4e2d\u5141\u8bb8\u9ea6\u514b\u98ce\u8bbf\u95ee',
  notFound: '\u6ca1\u6709\u68c0\u6d4b\u5230\u53ef\u7528\u9ea6\u514b\u98ce\uff0c\u8bf7\u68c0\u67e5\u8bbe\u5907\u662f\u5426\u5df2\u8fde\u63a5',
  inUse: '\u9ea6\u514b\u98ce\u53ef\u80fd\u88ab\u5176\u4ed6\u5e94\u7528\u5360\u7528\uff0c\u8bf7\u5173\u95ed\u5360\u7528\u540e\u91cd\u8bd5',
  constrained: '\u5f53\u524d\u97f3\u9891\u8bbe\u5907\u4e0d\u6ee1\u8db3\u91c7\u96c6\u6761\u4ef6\uff0c\u8bf7\u66f4\u6362\u9ea6\u514b\u98ce\u540e\u91cd\u8bd5',
  unsupported: '\u5f53\u524d\u9875\u9762\u73af\u5883\u4e0d\u652f\u6301\u9ea6\u514b\u98ce\u91c7\u96c6\uff0c\u8bf7\u4f7f\u7528\u6700\u65b0\u7248\u6d4f\u89c8\u5668',
  insecureContext: '\u5f53\u524d\u9875\u9762\u4e0d\u662f\u5b89\u5168\u4e0a\u4e0b\u6587\uff0c\u624b\u673a\u8bbf\u95ee\u65f6\u8bf7\u4f7f\u7528 HTTPS\uff1b\u672c\u673a\u8c03\u8bd5\u53ef\u4f7f\u7528 localhost',
  signaling: '\u8fde\u63a5\u4fe1\u4ee4\u670d\u52a1\u5668\u5931\u8d25',
  server: '\u4fe1\u4ee4\u670d\u52a1\u8fd4\u56de\u9519\u8bef',
} as const

interface User {
  id: string
  username: string
  isMuted: boolean
  isSpeaking: boolean
}

interface VoiceRoomState {
  localStream: MediaStream | null
  remoteStream: MediaStream | null
  users: User[]
  isConnected: boolean
  isMuted: boolean
  isSpeakerOn: boolean
  error: string | null
}

interface UseVoiceRoomReturn extends VoiceRoomState {
  joinRoom: (roomId: string, username: string) => Promise<boolean>
  leaveRoom: () => void
  toggleMute: () => void
  toggleSpeaker: () => void
}

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

export function useVoiceRoom(): UseVoiceRoomReturn {
  const [state, setState] = useState<VoiceRoomState>({
    localStream: null,
    remoteStream: null,
    users: [],
    isConnected: false,
    isMuted: false,
    isSpeakerOn: true,
    error: null,
  })

  const wsRef = useRef<WebSocket | null>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  const localStreamRef = useRef<MediaStream | null>(null)
  const currentRoomIdRef = useRef<string>('')
  const currentUsernameRef = useRef<string>('')

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
      if (event.candidate && wsRef.current) {
        wsRef.current.send(JSON.stringify({
          type: 'ice',
          room_id: currentRoomIdRef.current,
          payload: event.candidate,
        }))
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
        await pc.setRemoteDescription(new RTCSessionDescription(data.payload))
        break

      case 'ice':
        console.log('Received ICE candidate')
        await pc.addIceCandidate(new RTCIceCandidate(data.payload))
        break

      case 'user_left':
        console.log('Remote user left room')
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

  const joinRoom = useCallback(async (roomId: string, username: string) => {
    currentRoomIdRef.current = roomId
    currentUsernameRef.current = username

    const stream = await initLocalStream()
    if (!stream) return false

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    // Use the current page origin so Vite's /ws proxy can handle local HTTP/HTTPS switching.
    const wsUrl = `${protocol}//${window.location.host}/ws`

    console.log('Connecting WebSocket:', wsUrl)
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      console.log('WebSocket connected')
      ws.send(JSON.stringify({
        type: 'join',
        room_id: roomId,
        payload: username,
      }))
    }

    ws.onmessage = event => {
      console.log('Received signaling message:', event.data)
      const data = JSON.parse(event.data)
      handleSignaling(data)
    }

    ws.onerror = error => {
      console.error('WebSocket connection failed:', error)
      setState(prev => ({ ...prev, error: MICROPHONE_ERROR_MESSAGES.signaling }))
    }

    ws.onclose = () => {
      console.log('WebSocket closed')
    }

    createPeerConnection()
    return true
  }, [initLocalStream, handleSignaling, createPeerConnection])

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
      users: [],
      isConnected: false,
      isMuted: false,
      isSpeakerOn: true,
      error: null,
    })
  }, [])

  const toggleMute = useCallback(() => {
    if (localStreamRef.current) {
      const audioTrack = localStreamRef.current.getAudioTracks()[0]
      if (audioTrack) {
        audioTrack.enabled = state.isMuted
        setState(prev => ({ ...prev, isMuted: !prev.isMuted }))
      }
    }
  }, [state.isMuted])

  const toggleSpeaker = useCallback(() => {
    setState(prev => ({ ...prev, isSpeakerOn: !prev.isSpeakerOn }))
  }, [])

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
