import { useState, useEffect, useRef, useCallback } from 'react'

const ICE_SERVERS: RTCConfiguration = {
  iceServers: [
    { urls: 'stun:stun.l.google.com:19302' },
    { urls: 'stun:stun1.l.google.com:19302' },
  ]
}

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
      let message = '无法访问麦克风，请检查浏览器权限和设备状态'

      if (error.name === 'NotAllowedError') {
        message = '麦克风权限被拒绝，请在浏览器地址栏中允许麦克风访问'
      } else if (error.name === 'NotFoundError' || error.message === 'NO_AUDIO_TRACK') {
        message = '没有检测到可用麦克风，请检查设备是否已连接'
      } else if (error.name === 'NotReadableError') {
        message = '麦克风可能被其他应用占用，请关闭占用后重试'
      } else if (error.name === 'OverconstrainedError') {
        message = '当前音频设备不满足采集条件，请更换麦克风后重试'
      } else if (error.message === 'MEDIA_DEVICES_UNAVAILABLE') {
        message = '当前页面环境不支持麦克风采集，请使用最新版浏览器'
      } else if (!window.isSecureContext && window.location.hostname !== 'localhost') {
        message = '当前页面不是安全上下文，麦克风采集需要 HTTPS 或 localhost'
      }

      setState(prev => ({ ...prev, localStream: null, error: message }))
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

    pc.ontrack = (event) => {
      console.log('Received remote media stream')
      setState(prev => ({ ...prev, remoteStream: event.streams[0] }))
    }

    pc.onicecandidate = (event) => {
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
          error: data?.payload?.message || '信令服务返回错误',
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
    const wsHost = import.meta.env.DEV
      ? `${window.location.hostname}:8080`
      : 'localhost:8080'
    const wsUrl = `${protocol}//${wsHost}/ws`

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

    ws.onmessage = (event) => {
      console.log('Received signaling message:', event.data)
      const data = JSON.parse(event.data)
      handleSignaling(data)
    }

    ws.onerror = (error) => {
      console.error('WebSocket connection failed:', error)
      setState(prev => ({ ...prev, error: '连接信令服务器失败' }))
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
