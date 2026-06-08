import { useCallback, useMemo, useState } from 'react'

import InviteModal from './components/modals/InviteModal'
import SettingsModal from './components/modals/SettingsModal'
import { useVoiceRoom } from './hooks/useVoiceRoom'
import HomePage from './pages/HomePage'
import LeavePage from './pages/LeavePage'
import VoiceRoomPage from './pages/VoiceRoomPage'
import type { AppView, JoinRoomInput, LeaveRoomSummary } from './types/voiceRoomUi'

// getInitialRoomId 从邀请链接里读取房间号，让朋友打开链接后可以直接看到预填房间。
function getInitialRoomId() {
  if (typeof window === 'undefined') return ''

  return new URLSearchParams(window.location.search).get('room')?.toUpperCase() || ''
}

// createRoomId 在没有输入房间号时生成一个短房间号，便于口头分享。
function createRoomId() {
  return Math.random().toString(36).slice(2, 8).toUpperCase()
}

// formatDuration 把毫秒转换成用户容易理解的停留时长。
function formatDuration(durationMs: number) {
  const totalSeconds = Math.max(1, Math.floor(durationMs / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60

  if (minutes === 0) return `${seconds} 秒`

  return `${minutes} 分 ${seconds} 秒`
}

// App 负责页面级状态和弹窗开关，真实语音连接仍交给 useVoiceRoom 管理。
function App() {
  const [view, setView] = useState<AppView>('home')
  const [roomId, setRoomId] = useState(getInitialRoomId)
  const [username, setUsername] = useState('')
  const [isHost, setIsHost] = useState(false)
  const [isJoining, setIsJoining] = useState(false)
  const [joinedAt, setJoinedAt] = useState<number | null>(null)
  const [isInviteOpen, setIsInviteOpen] = useState(false)
  const [isSettingsOpen, setIsSettingsOpen] = useState(false)
  const [isMembersOpen, setIsMembersOpen] = useState(false)
  const [lastSummary, setLastSummary] = useState<LeaveRoomSummary | null>(null)

  const voiceRoom = useVoiceRoom()

  const onlineMemberCount = useMemo(() => {
    return Math.max(1, voiceRoom.users.length)
  }, [voiceRoom.users.length])

  const enterRoom = useCallback(
    async ({ roomId: inputRoomId, username: inputUsername }: JoinRoomInput, nextIsHost: boolean) => {
      const nextRoomId = inputRoomId || createRoomId()

      setIsJoining(true)
      const joined = await voiceRoom.joinRoom(nextRoomId, inputUsername)
      setIsJoining(false)

      if (!joined) return

      setRoomId(nextRoomId)
      setUsername(inputUsername)
      setIsHost(nextIsHost)
      setJoinedAt(Date.now())
      setView('room')
    },
    [voiceRoom],
  )

  const handleCreateRoom = useCallback(
    (input: JoinRoomInput) => {
      void enterRoom(input, true)
    },
    [enterRoom],
  )

  const handleJoinRoom = useCallback(
    (input: JoinRoomInput) => {
      void enterRoom(input, false)
    },
    [enterRoom],
  )

  const handleLeave = useCallback(() => {
    const durationText = joinedAt ? formatDuration(Date.now() - joinedAt) : '刚刚'

    setLastSummary({
      roomId,
      username,
      durationText,
      memberCount: onlineMemberCount,
    })

    voiceRoom.leaveRoom()
    setIsInviteOpen(false)
    setIsSettingsOpen(false)
    setIsMembersOpen(false)
    setJoinedAt(null)
    setView('left')
  }, [joinedAt, onlineMemberCount, roomId, username, voiceRoom])

  const handleRejoin = useCallback(() => {
    if (!lastSummary) return

    void enterRoom(
      {
        roomId: lastSummary.roomId,
        username: lastSummary.username,
      },
      isHost,
    )
  }, [enterRoom, isHost, lastSummary])

  const handleGoHome = useCallback(() => {
    setRoomId('')
    setUsername('')
    setIsHost(false)
    setLastSummary(null)
    setView('home')
  }, [])

  return (
    <div className="app-shell">
      {view === 'home' && (
        <HomePage
          defaultRoomId={roomId}
          defaultUsername={username}
          error={voiceRoom.error}
          isJoining={isJoining}
          onCreateRoom={handleCreateRoom}
          onJoinRoom={handleJoinRoom}
          onOpenSettings={() => setIsSettingsOpen(true)}
        />
      )}

      {view === 'room' && (
        <VoiceRoomPage
          roomId={roomId}
          username={username}
          isHost={isHost}
          users={voiceRoom.users}
          localStream={voiceRoom.localStream}
          remoteStream={voiceRoom.remoteStream}
          isConnected={voiceRoom.isConnected}
          isMuted={voiceRoom.isMuted}
          isSpeakerOn={voiceRoom.isSpeakerOn}
          error={voiceRoom.error}
          isMembersOpen={isMembersOpen}
          onCloseMembers={() => setIsMembersOpen(false)}
          onToggleMute={voiceRoom.toggleMute}
          onToggleSpeaker={voiceRoom.toggleSpeaker}
          onOpenMembers={() => setIsMembersOpen(true)}
          onOpenInvite={() => setIsInviteOpen(true)}
          onOpenSettings={() => setIsSettingsOpen(true)}
          onLeave={handleLeave}
        />
      )}

      {view === 'left' && <LeavePage summary={lastSummary} onRejoin={handleRejoin} onHome={handleGoHome} />}

      <InviteModal
        isOpen={isInviteOpen}
        roomId={roomId}
        hostName={username}
        memberCount={onlineMemberCount}
        onClose={() => setIsInviteOpen(false)}
      />

      <SettingsModal
        isOpen={isSettingsOpen}
        hasMicrophone={Boolean(voiceRoom.localStream)}
        isMuted={voiceRoom.isMuted}
        isSpeakerOn={voiceRoom.isSpeakerOn}
        onToggleMute={voiceRoom.toggleMute}
        onToggleSpeaker={voiceRoom.toggleSpeaker}
        onClose={() => setIsSettingsOpen(false)}
      />
    </div>
  )
}

export default App
