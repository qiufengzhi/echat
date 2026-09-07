import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import HostTransferModal from '../components/modals/HostTransferModal'
import { useVoiceRoom } from '../hooks/useVoiceRoom'
import HomePage from './HomePage'
import LeavePage from './LeavePage'
import VoiceRoomPage from './VoiceRoomPage'
import { getAuthUser, logout } from '../services/auth'
import type { AuthUser } from '../services/auth'
import type { AppView, JoinRoomInput, LeaveRoomSummary } from '../types/voiceRoomUi'

// ROOM_SESSION_KEY 用于页面刷新后恢复房间状态，避免重连或手动刷新后直接回到主界面
const ROOM_SESSION_KEY = 'voice_room_session'

// RoomSession 描述需要持久化的房间会话信息
interface RoomSession {
  view: AppView // 当前页面视图
  roomId: string // 当前房间号
  username: string // 当前用户昵称
}

// getInitialSession 读取 sessionStorage 中保存的房间会话，用于刷新后恢复
function getInitialSession(): Partial<RoomSession> {
  if (typeof window === 'undefined') return {}
  const saved = sessionStorage.getItem(ROOM_SESSION_KEY)
  if (!saved) return {}

  try {
    return JSON.parse(saved) as RoomSession
  } catch {
    sessionStorage.removeItem(ROOM_SESSION_KEY)
    return {}
  }
}

function getInitialRoomId() {
  if (typeof window === 'undefined') return ''
  return new URLSearchParams(window.location.search).get('room')?.toUpperCase() || ''
}

function createRoomId() {
  return Math.random().toString(36).slice(2, 8).toUpperCase()
}

function formatDuration(durationMs: number) {
  const totalSeconds = Math.max(1, Math.floor(durationMs / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes === 0) return `${seconds} 秒`
  return `${minutes} 分 ${seconds} 秒`
}

// LegacyFlow 是改版过渡期的旧首页/房间/离开三视图状态机（在路由化后整体挂到 "/"）。
// 登录已拆到 /login 页，这里仅保留下载已登录态的进房能力，阶段 3 用 ChannelPage 替换整段
export default function LegacyFlow() {
  const navigate = useNavigate()
  const initialSession = getInitialSession()
  const [view, setView] = useState<AppView>(initialSession.view || 'home')
  const [roomId, setRoomId] = useState(initialSession.roomId || getInitialRoomId)
  const [username, setUsername] = useState(initialSession.username || getAuthUser()?.display_name || '')
  const [isJoining, setIsJoining] = useState(false)
  const [joinedAt, setJoinedAt] = useState<number | null>(null)
  const [isHostTransferOpen, setIsHostTransferOpen] = useState(false)
  const [lastSummary, setLastSummary] = useState<LeaveRoomSummary | null>(null)
  // 认证态由 /login 路由维护，这里只从本地缓存读取用户概要供首页展示
  const [authedUser, setAuthedUser] = useState<AuthUser | null>(() => getAuthUser())
  // kickedNotice 连接被服务端踢下线后的提示文案，切回首页展示
  const [kickedNotice, setKickedNotice] = useState<string | null>(null)

  const voiceRoom = useVoiceRoom({
    onKicked: () => {
      setKickedNotice('你已被强制下线（账号在别处登录，或会话已被注销）')
      setIsHostTransferOpen(false)
      setLastSummary(null)
      setJoinedAt(null)
      setView('home')
    },
  })

  // handleLogout 清除本地令牌并跳转登录页
  const handleLogout = useCallback(() => {
    logout()
    setAuthedUser(null)
    setKickedNotice(null)
    navigate('/login', { replace: true })
  }, [navigate])

  // 过渡期首页的登录入口统一指向 /login 页
  const handleGoLogin = useCallback(() => navigate('/login'), [navigate])

  // 动态标题：房间页带上昵称
  useEffect(() => {
    document.title = view === 'room' ? `(っ´▽｀)っ ${username}` : '(っ´▽｀)っ'
  }, [view, username])

  const onlineMemberCount = useMemo(
    () => Math.max(1, voiceRoom.users.length),
    [voiceRoom.users.length],
  )

  const hostTransferCandidates = useMemo(
    () =>
      voiceRoom.users
        .filter(user => user.id !== voiceRoom.hostId)
        .map(user => ({ id: user.id, name: user.username })),
    [voiceRoom.hostId, voiceRoom.users],
  )

  const enterRoom = useCallback(
    async ({ roomId: inputRoomId, username: inputUsername }: JoinRoomInput) => {
      const nextRoomId = inputRoomId || createRoomId()
      setIsJoining(true)
      const joined = await voiceRoom.joinRoom(nextRoomId, inputUsername)
      setIsJoining(false)
      if (!joined) return
      setRoomId(nextRoomId)
      setUsername(inputUsername)
      setJoinedAt(Date.now())
      setView('room')
    },
    [voiceRoom],
  )

  // 页面刷新后自动恢复房间会话，避免重连失败或用户手动刷新后直接回到主界面
  const hasRestoredRef = useRef(false)
  useEffect(() => {
    if (hasRestoredRef.current) return
    hasRestoredRef.current = true

    const saved = sessionStorage.getItem(ROOM_SESSION_KEY)
    if (!saved) return

    try {
      const session = JSON.parse(saved) as RoomSession
      if (session.view === 'room' && session.roomId && session.username) {
        void enterRoom({ roomId: session.roomId, username: session.username })
      }
    } catch {
      sessionStorage.removeItem(ROOM_SESSION_KEY)
    }
  }, [enterRoom])

  // 当前在房间时持久化会话，离开/返回主界面时清理
  const isInitialRenderRef = useRef(true)
  useEffect(() => {
    if (isInitialRenderRef.current) {
      isInitialRenderRef.current = false
      return
    }

    if (view === 'room' && roomId && username) {
      const session: RoomSession = { view, roomId, username }
      sessionStorage.setItem(ROOM_SESSION_KEY, JSON.stringify(session))
    } else {
      sessionStorage.removeItem(ROOM_SESSION_KEY)
    }
  }, [view, roomId, username])

  const finalizeLeave = useCallback(
    (nextHostId?: string) => {
      const durationText = joinedAt ? formatDuration(Date.now() - joinedAt) : '刚刚'
      setLastSummary({ roomId, username, durationText, memberCount: onlineMemberCount })
      voiceRoom.leaveRoom(nextHostId)
      setIsHostTransferOpen(false)
      setJoinedAt(null)
      setView('left')
    },
    [joinedAt, onlineMemberCount, roomId, username, voiceRoom],
  )

  const handleLeave = useCallback(() => {
    if (voiceRoom.isHost && hostTransferCandidates.length > 0) {
      setIsHostTransferOpen(true)
      return
    }
    finalizeLeave()
  }, [finalizeLeave, hostTransferCandidates.length, voiceRoom.isHost])

  const handleRejoin = useCallback(() => {
    if (!lastSummary) return
    void enterRoom({ roomId: lastSummary.roomId, username: lastSummary.username })
  }, [enterRoom, lastSummary])

  const handleGoHome = useCallback(() => {
    setRoomId('')
    setUsername('')
    setLastSummary(null)
    setIsHostTransferOpen(false)
    setView('home')
  }, [])

  return (
    <>
      {view === 'home' && (
        <HomePage
          defaultRoomId={roomId}
          defaultUsername={username}
          error={voiceRoom.error || kickedNotice}
          isJoining={isJoining}
          onCreateRoom={input => { void enterRoom(input) }}
          onJoinRoom={input => { void enterRoom(input) }}
          authedUser={authedUser}
          loggingIn={false}
          loginError={null}
          onLogin={handleGoLogin}
          onLogout={handleLogout}
        />
      )}

      {view === 'room' && (
        <VoiceRoomPage
          roomId={roomId}
          username={username}
          hostId={voiceRoom.hostId}
          isHost={voiceRoom.isHost}
          users={voiceRoom.users}
          localStream={voiceRoom.localStream}
          remoteStreams={voiceRoom.remoteStreams}
          isConnected={voiceRoom.isConnected}
          isReconnecting={voiceRoom.isReconnecting}
          isMuted={voiceRoom.isMuted}
          isSpeakerOn={voiceRoom.isSpeakerOn}
          isAIEnabled={voiceRoom.isAIEnabled}
          aiState={voiceRoom.aiState}
          error={voiceRoom.error}
          onToggleMute={voiceRoom.toggleMute}
          onToggleSpeaker={voiceRoom.toggleSpeaker}
          onToggleAI={voiceRoom.toggleAI}
          onLeave={handleLeave}
        />
      )}

      {view === 'left' && (
        <LeavePage summary={lastSummary} onRejoin={handleRejoin} onHome={handleGoHome} />
      )}

      <HostTransferModal
        isOpen={isHostTransferOpen}
        candidates={hostTransferCandidates}
        onClose={() => setIsHostTransferOpen(false)}
        onConfirm={nextHostId => finalizeLeave(nextHostId)}
        onRandom={() => finalizeLeave()}
      />
    </>
  )
}