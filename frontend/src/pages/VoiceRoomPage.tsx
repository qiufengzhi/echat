import { useCallback, useMemo } from 'react'

import ControlDock from '../components/room/ControlDock'
import MemberSeatGrid from '../components/room/MemberSeatGrid'
import RemoteAudio from '../components/room/RemoteAudio'
import RoomTopBar from '../components/room/RoomTopBar'
import type { RoomStatusCopy, VoiceRoomMember } from '../types/voiceRoomUi'

interface HookUser {
  id: string
  username: string
  isMuted: boolean
  isSpeaking: boolean
}

interface VoiceRoomPageProps {
  roomId: string
  username: string
  hostId: string | null
  isHost: boolean
  users: HookUser[]
  localStream: MediaStream | null
  remoteStreams: Map<string, MediaStream>
  isConnected: boolean
  isMuted: boolean
  isSpeakerOn: boolean
  isAIEnabled: boolean
  error: string | null
  onToggleMute: () => void
  onToggleSpeaker: () => void
  onToggleAI: () => void
  onLeave: () => void
}
function buildMembers(
  users: HookUser[],
  username: string,
  hostId: string | null,
  isHost: boolean,
  isMuted: boolean,
  isConnected: boolean,
  remoteStreams: Map<string, MediaStream>,
): VoiceRoomMember[] {
  const knownUsers = users.length > 0 ? users : []
  const hasSelfInUsers = knownUsers.some(u => u.username === username)

  const realMembers: VoiceRoomMember[] = knownUsers.map(user => ({
    id: user.id,
    name: user.username,
    role: user.id === hostId ? 'host' : 'member',
    isSelf: user.username === username,
    isMuted: user.username === username ? isMuted : user.isMuted,
    isSpeaking: user.username === username ? !isMuted && isConnected : user.isSpeaking,
    isOnline: true,
    hasAudio: remoteStreams.has(user.id),
  }))

  if (!hasSelfInUsers) {
    realMembers.unshift({
      id: 'local-user',
      name: username || '我',
      role: isHost ? 'host' : 'member',
      isSelf: true,
      isMuted,
      isSpeaking: !isMuted && isConnected,
      isOnline: true,
      hasAudio: false,
    })
  }

  if (remoteStreams.size > 0 && realMembers.length === 1 && realMembers[0]?.isSelf) {
    remoteStreams.forEach((_stream, userId) => {
      realMembers.push({
        id: userId,
        name: '朋友',
        role: 'member',
        isSelf: false,
        isMuted: false,
        isSpeaking: true,
        isOnline: true,
        hasAudio: true,
      })
    })
  }

  // 始终保留一个空席位作为邀请入口
  const emptySeat: VoiceRoomMember = {
    id: 'empty-invite',
    name: '空席位',
    role: 'empty',
    isSelf: false,
    isMuted: false,
    isSpeaking: false,
    isOnline: false,
    hasAudio: false,
  }

  return [...realMembers, emptySeat]
}

function getRoomStatus(isConnected: boolean, error: string | null): RoomStatusCopy {
  if (error) return { connectionText: '重连中', qualityText: '正在恢复', tone: 'reconnecting' }
  if (isConnected) return { connectionText: '已连上', qualityText: '声音流畅', tone: 'ready' }
  return { connectionText: '准备中', qualityText: '等朋友进来', tone: 'waiting' }
}

export default function VoiceRoomPage({
  roomId,
  username,
  hostId,
  isHost,
  users,
  remoteStreams,
  isConnected,
  isMuted,
  isSpeakerOn,
  isAIEnabled,
  error,
  onToggleMute,
  onToggleSpeaker,
  onToggleAI,
  onLeave,
}: VoiceRoomPageProps) {
  const members = useMemo(
    () => buildMembers(users, username, hostId, isHost, isMuted, isConnected, remoteStreams),
    [users, username, hostId, isHost, isMuted, isConnected, remoteStreams],
  )
  const onlineCount = members.filter(m => m.isOnline).length
  const status = getRoomStatus(isConnected, error)

  const handleInvite = useCallback(() => {
    const link = `${window.location.origin}${window.location.pathname}?room=${roomId}`
    navigator.clipboard.writeText(link).catch(() => {})
  }, [roomId])

  return (
    <main className="voice-room-page">
      {Array.from(remoteStreams.entries()).map(([userId, stream]) => (
        <RemoteAudio key={userId} stream={stream} isSpeakerOn={isSpeakerOn} />
      ))}

      <RoomTopBar
        roomId={roomId}
        memberCount={onlineCount}
        status={status}
      />

      {error && (
        <div className="reconnect-banner">
          🔄 连接不太稳，正在帮你恢复…
        </div>
      )}

      <MemberSeatGrid members={members} onInvite={handleInvite} />

      {error && <div className="room-error">{error}</div>}

      <ControlDock
        isMuted={isMuted}
        isSpeakerOn={isSpeakerOn}
        isHost={isHost}
        isAIEnabled={isAIEnabled}
        onToggleMute={onToggleMute}
        onToggleSpeaker={onToggleSpeaker}
        onToggleAI={onToggleAI}
        onLeave={onLeave}
      />
    </main>
  )
}
