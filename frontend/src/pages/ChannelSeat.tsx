import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import HostTransferModal from '../components/modals/HostTransferModal'
import { useVoiceRoom } from '../hooks/useVoiceRoom'
import { getAuthUser } from '../services/auth'
import VoiceRoomPage from './VoiceRoomPage'

// ChannelSeat 是主界面「创建/加入频道」通往语音房的临时承载视图：
// 在 /channel/:roomId 上用原有 VoiceRoomPage 直接进屋，阶段 3b 由重新设计的 ChannelPage 替换
export default function ChannelSeat() {
  const { roomId } = useParams<{ roomId: string }>()
  const navigate = useNavigate()
  const user = getAuthUser()
  const username = user?.display_name || user?.username || ''
  const [joinError, setJoinError] = useState<string | null>(null) // 进房失败时的提示文案
  const [isHostTransferOpen, setIsHostTransferOpen] = useState(false) // 房主离开交接弹窗开关

  const voiceRoom = useVoiceRoom({
    onKicked: () => {
      setIsHostTransferOpen(false)
      navigate('/', { replace: true })
    },
  })

  // 进入页面即加入频道：房间号来自路由参数，昵称取登录用户展示名
  const joinedRef = useRef(false)
  useEffect(() => {
    if (!roomId || joinedRef.current) return
    joinedRef.current = true
    void (async () => {
      const ok = await voiceRoom.joinRoom(roomId.toUpperCase(), username)
      if (!ok) setJoinError('无法连接频道，请确认服务可用后重试')
    })()
  }, [roomId, username, voiceRoom])

  const hostTransferCandidates = useMemo(
    () =>
      voiceRoom.users
        .filter(item => item.id !== voiceRoom.hostId)
        .map(item => ({ id: item.id, name: item.username })),
    [voiceRoom.hostId, voiceRoom.users],
  )

  const leaveToHome = useCallback(
    (nextHostId?: string) => {
      voiceRoom.leaveRoom(nextHostId)
      navigate('/', { replace: true })
    },
    [navigate, voiceRoom],
  )

  const handleLeave = useCallback(() => {
    // 房主离开且还有其他成员时先走交接弹窗，避免房主身份静默丢失
    if (voiceRoom.isHost && hostTransferCandidates.length > 0) {
      setIsHostTransferOpen(true)
      return
    }
    leaveToHome()
  }, [hostTransferCandidates.length, leaveToHome, voiceRoom.isHost])

  return (
    <main className="voice-room-page">
      <VoiceRoomPage
        roomId={roomId?.toUpperCase() || ''}
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
        error={voiceRoom.error || joinError}
        onToggleMute={voiceRoom.toggleMute}
        onToggleSpeaker={voiceRoom.toggleSpeaker}
        onToggleAI={voiceRoom.toggleAI}
        onLeave={handleLeave}
      />
      <HostTransferModal
        isOpen={isHostTransferOpen}
        candidates={hostTransferCandidates}
        onClose={() => setIsHostTransferOpen(false)}
        onConfirm={nextHostId => leaveToHome(nextHostId)}
        onRandom={() => leaveToHome()}
      />
    </main>
  )
}