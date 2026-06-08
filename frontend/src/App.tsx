import { useCallback, useMemo, useState } from 'react'

import HostTransferModal from './components/modals/HostTransferModal'
import InviteModal from './components/modals/InviteModal'
import SettingsModal from './components/modals/SettingsModal'
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
  const [isInviteOpen, setIsInviteOpen] = useState(false)
  const [isSettingsOpen, setIsSettingsOpen] = useState(false)
  const [isMembersOpen, setIsMembersOpen] = useState(false)
  const [isHostTransferOpen, setIsHostTransferOpen] = useState(false)
  const [lastSummary, setLastSummary] = useState<LeaveRoomSummary | null>(null)

  const voiceRoom = useVoiceRoom()

  const onlineMemberCount = useMemo(() => {
    return Math.max(1, voiceRoom.users.length)
  }, [voiceRoom.users.length])

  const hostName = useMemo(() => {
    return voiceRoom.users.find(user => user.id === voiceRoom.hostId)?.username || (voiceRoom.isHost ? username : '')
  }, [voiceRoom.hostId, voiceRoom.isHost, voiceRoom.users, username])

  const hostTransferCandidates = useMemo(() => {
    return voiceRoom.users
      .filter(user => user.id !== voiceRoom.hostId)
      .map(user => ({
        id: user.id,
        name: user.username,
      }))
  }, [voiceRoom.hostId, voiceRoom.users])

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

  const handleCreateRoom = useCallback(
    (input: JoinRoomInput) => {
      void enterRoom(input)
    },
    [enterRoom],
  )

  const handleJoinRoom = useCallback(
    (input: JoinRoomInput) => {
      void enterRoom(input)
    },
    [enterRoom],
  )

  const finalizeLeave = useCallback(
    (nextHostId?: string) => {
      const durationText = joinedAt ? formatDuration(Date.now() - joinedAt) : '刚刚'

      setLastSummary({
        roomId,
        username,
        durationText,
        memberCount: onlineMemberCount,
      })

      voiceRoom.leaveRoom(nextHostId)
      setIsInviteOpen(false)
      setIsSettingsOpen(false)
      setIsMembersOpen(false)
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

    void enterRoom({
      roomId: lastSummary.roomId,
      username: lastSummary.username,
    })
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
          onCreateRoom={handleCreateRoom}
          onJoinRoom={handleJoinRoom}
          onOpenSettings={() => setIsSettingsOpen(true)}
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
        hostName={hostName}
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
