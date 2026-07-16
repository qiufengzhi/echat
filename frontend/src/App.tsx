import { useCallback, useMemo, useState } from 'react'

import HostTransferModal from './components/modals/HostTransferModal'
import InviteModal from './components/modals/InviteModal'
import SettingsModal from './components/modals/SettingsModal'
import { useVoiceRoom } from './hooks/useVoiceRoom'
import HomePage from './pages/HomePage'
import LeavePage from './pages/LeavePage'
import VoiceRoomPage from './pages/VoiceRoomPage'
import type { AppView, JoinRoomInput, LeaveRoomSummary } from './types/voiceRoomUi'

// App 是 eChat 前端的页面编排层：
// 它负责首页/房间/离开页切换和弹窗开关，真正的麦克风、WebSocket、WebRTC 生命周期都交给 useVoiceRoom

// getInitialRoomId 从邀请链接里读取房间号，让朋友打开链接后可以直接看到预填房间
function getInitialRoomId() {
  if (typeof window === 'undefined') return ''

  return new URLSearchParams(window.location.search).get('room')?.toUpperCase() || ''
}

// createRoomId 在没有输入房间号时生成一个短房间号，便于口头分享
function createRoomId() {
  return Math.random().toString(36).slice(2, 8).toUpperCase()
}

// formatDuration 把毫秒转换成用户容易理解的停留时长
function formatDuration(durationMs: number) {
  const totalSeconds = Math.max(1, Math.floor(durationMs / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60

  if (minutes === 0) return `${seconds} 秒`

  return `${minutes} 分 ${seconds} 秒`
}

// App 负责页面级状态和弹窗开关，实时通信状态以后端和 useVoiceRoom 为准
function App() {
  // view 决定当前展示哪个主页面。项目当前控制在首页、声聊间、离开页三段流程内
  const [view, setView] = useState<AppView>('home')
  // roomId/username 是页面级输入值，加入成功后才会成为当前房间上下文
  const [roomId, setRoomId] = useState(getInitialRoomId)
  const [username, setUsername] = useState('')
  // isJoining 用来锁住创建/加入按钮，避免重复申请麦克风或重复连房
  const [isJoining, setIsJoining] = useState(false)
  // joinedAt 只用于离开页展示本次停留时长，不参与连接逻辑
  const [joinedAt, setJoinedAt] = useState<number | null>(null)
  // 这些布尔值只控制 UI 展示，不直接影响底层连接
  const [isInviteOpen, setIsInviteOpen] = useState(false)
  const [isSettingsOpen, setIsSettingsOpen] = useState(false)
  const [isMembersOpen, setIsMembersOpen] = useState(false)
  // 房主离开且房间内还有其他成员时，先展示交接弹窗再真正离开
  const [isHostTransferOpen, setIsHostTransferOpen] = useState(false)
  // lastSummary 保存“刚离开的房间”摘要，支持离开页重新加入和展示收尾信息
  const [lastSummary, setLastSummary] = useState<LeaveRoomSummary | null>(null)

  const voiceRoom = useVoiceRoom()

  // 还没收到后端成员列表时，页面至少应把当前用户算作 1 人，避免人数显示成 0
  const onlineMemberCount = useMemo(() => {
    return Math.max(1, voiceRoom.users.length)
  }, [voiceRoom.users.length])

  // 房主名称以后端 hostId 匹配到的成员为准；首位用户等待阶段用当前昵称兜底
  const hostName = useMemo(() => {
    return voiceRoom.users.find(user => user.id === voiceRoom.hostId)?.username || (voiceRoom.isHost ? username : '')
  }, [voiceRoom.hostId, voiceRoom.isHost, voiceRoom.users, username])

  // 房主交接候选只包含在线且不是当前房主的成员
  const hostTransferCandidates = useMemo(() => {
    return voiceRoom.users
      .filter(user => user.id !== voiceRoom.hostId)
      .map(user => ({
        id: user.id,
        name: user.username,
      }))
  }, [voiceRoom.hostId, voiceRoom.users])

  // enterRoom 是创建和加入共用的进入流程：
  // 先让 hook 完成麦克风和房间连接，再更新页面上下文，避免进入房间页后才发现加入失败
  const enterRoom = useCallback(
    async ({ roomId: inputRoomId, username: inputUsername }: JoinRoomInput) => {
      const nextRoomId = inputRoomId || createRoomId()

      setIsJoining(true)
      const joined = await voiceRoom.joinRoom(nextRoomId, inputUsername)
      setIsJoining(false)

      if (!joined) return

      // 只有真实加入成功后才切到房间页，这样首页可以继续展示加入失败原因
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
      // 离开前先记录摘要；随后 hook 会清理实时连接和成员列表
      const durationText = joinedAt ? formatDuration(Date.now() - joinedAt) : '刚刚'

      setLastSummary({
        roomId,
        username,
        durationText,
        memberCount: onlineMemberCount,
      })

      voiceRoom.leaveRoom(nextHostId)
      // 离开后关闭所有浮层，避免用户回到离开页时还残留邀请/设置/成员抽屉
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
    // 当前用户是房主且房间里还有其他人时，先让用户选择是否指定下一任房主
    if (voiceRoom.isHost && hostTransferCandidates.length > 0) {
      setIsHostTransferOpen(true)
      return
    }

    finalizeLeave()
  }, [finalizeLeave, hostTransferCandidates.length, voiceRoom.isHost])

  const handleRejoin = useCallback(() => {
    if (!lastSummary) return

    // 重新加入复用离开前的房间号和昵称，减少用户重复输入
    void enterRoom({
      roomId: lastSummary.roomId,
      username: lastSummary.username,
    })
  }, [enterRoom, lastSummary])

  const handleGoHome = useCallback(() => {
    // 回到首页代表重新开始一次流程，因此清掉上一次房间上下文
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
          remoteStreams={voiceRoom.remoteStreams}
          isConnected={voiceRoom.isConnected}
          isMuted={voiceRoom.isMuted}
          isSpeakerOn={voiceRoom.isSpeakerOn}
          isAIEnabled={voiceRoom.isAIEnabled}
          error={voiceRoom.error}
          isMembersOpen={isMembersOpen}
          onCloseMembers={() => setIsMembersOpen(false)}
          onToggleMute={voiceRoom.toggleMute}
          onToggleSpeaker={voiceRoom.toggleSpeaker}
          onToggleAI={voiceRoom.toggleAI}
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
        availableMicrophones={voiceRoom.availableMicrophones}
        availableSpeakers={voiceRoom.availableSpeakers}
        currentMicrophoneId={voiceRoom.currentMicrophoneId}
        onToggleMute={voiceRoom.toggleMute}
        onToggleSpeaker={voiceRoom.toggleSpeaker}
        onRefreshDevices={voiceRoom.refreshAudioDevices}
        onSwitchMicrophone={voiceRoom.switchMicrophone}
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
