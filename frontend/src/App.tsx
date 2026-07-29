import { useCallback, useEffect, useMemo, useState } from 'react'

import HostTransferModal from './components/modals/HostTransferModal'
import { useVoiceRoom } from './hooks/useVoiceRoom'
import HomePage from './pages/HomePage'
import LeavePage from './pages/LeavePage'
import VoiceRoomPage from './pages/VoiceRoomPage'
import type { AppView, JoinRoomInput, LeaveRoomSummary } from './types/voiceRoomUi'

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

function App() {
  const [view, setView] = useState<AppView>('home')
  const [roomId, setRoomId] = useState(getInitialRoomId)
  const [username, setUsername] = useState('')
  const [isJoining, setIsJoining] = useState(false)
  const [joinedAt, setJoinedAt] = useState<number | null>(null)
  const [isHostTransferOpen, setIsHostTransferOpen] = useState(false)
  const [lastSummary, setLastSummary] = useState<LeaveRoomSummary | null>(null)

  const voiceRoom = useVoiceRoom()

  // 动态标题：首页/离开页仅颜文字，房间页加上昵称
  useEffect(() => {
    if (view === 'room') {
      document.title = `(っ´▽｀)っ ${username}`
    } else {
      document.title = '(っ´▽｀)っ'
    }
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
    <div className="app-shell">
      {view === 'home' && (
        <HomePage
          defaultRoomId={roomId}
          defaultUsername={username}
          error={voiceRoom.error}
          isJoining={isJoining}
          onCreateRoom={input => { void enterRoom(input) }}
          onJoinRoom={input => { void enterRoom(input) }}
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
          isMuted={voiceRoom.isMuted}
          isSpeakerOn={voiceRoom.isSpeakerOn}
          isAIEnabled={voiceRoom.isAIEnabled}
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
    </div>
  )
}

export default App
