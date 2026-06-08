import { useMemo } from 'react'

import ControlDock from '../components/room/ControlDock'
import MemberPanel from '../components/room/MemberPanel'
import MemberSeatGrid from '../components/room/MemberSeatGrid'
import RemoteAudio from '../components/room/RemoteAudio'
import RoomActivity from '../components/room/RoomActivity'
import RoomTopBar from '../components/room/RoomTopBar'
import type { RoomActivityItem, RoomStatusCopy, VoiceRoomMember } from '../types/voiceRoomUi'

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
  remoteStream: MediaStream | null
  isConnected: boolean
  isMuted: boolean
  isSpeakerOn: boolean
  error: string | null
  isMembersOpen: boolean
  onCloseMembers: () => void
  onToggleMute: () => void
  onToggleSpeaker: () => void
  onOpenMembers: () => void
  onOpenInvite: () => void
  onOpenSettings: () => void
  onLeave: () => void
}

const MAX_VISIBLE_SEATS = 8

function buildMembers({
  users,
  username,
  hostId,
  isHost,
  isMuted,
  isConnected,
  remoteStream,
}: Pick<
  VoiceRoomPageProps,
  'users' | 'username' | 'hostId' | 'isHost' | 'isMuted' | 'isConnected' | 'remoteStream'
>) {
  const knownUsers = users.length > 0 ? users : []
  const hasSelfInUsers = knownUsers.some(user => user.username === username)

  const realMembers: VoiceRoomMember[] = knownUsers.map(user => ({
    id: user.id,
    name: user.username,
    role: user.id === hostId ? 'host' : 'member',
    isSelf: user.username === username,
    isMuted: user.username === username ? isMuted : user.isMuted,
    isSpeaking: user.username === username ? !isMuted && Boolean(isConnected) : user.isSpeaking,
    isOnline: true,
  }))

  if (!hasSelfInUsers) {
    realMembers.unshift({
      id: 'local-user',
      name: username || '我',
      role: isHost ? 'host' : 'member',
      isSelf: true,
      isMuted,
      isSpeaking: !isMuted && Boolean(isConnected),
      isOnline: true,
    })
  }

  if (remoteStream && realMembers.length === 1) {
    realMembers.push({
      id: 'remote-user',
      name: '朋友',
      role: 'member',
      isSelf: false,
      isMuted: false,
      isSpeaking: true,
      isOnline: true,
    })
  }

  const emptySeatCount = Math.max(0, MAX_VISIBLE_SEATS - realMembers.length)
  const emptySeats: VoiceRoomMember[] = Array.from({ length: emptySeatCount }, (_, index) => ({
    id: `empty-seat-${index + 1}`,
    name: '空席位',
    role: 'empty',
    isSelf: false,
    isMuted: false,
    isSpeaking: false,
    isOnline: false,
  }))

  return [...realMembers, ...emptySeats]
}

function getRoomStatus(isConnected: boolean, localStream: MediaStream | null, error: string | null): RoomStatusCopy {
  if (error) {
    return {
      connectionText: '正在恢复',
      qualityText: '稍等一下',
      tone: 'reconnecting',
    }
  }

  if (isConnected) {
    return {
      connectionText: '已连上',
      qualityText: '声音很顺',
      tone: 'ready',
    }
  }

  return {
    connectionText: localStream ? '已开麦' : '准备中',
    qualityText: localStream ? '等朋友进来' : '先开麦克风',
    tone: 'waiting',
  }
}

export default function VoiceRoomPage({
  roomId,
  username,
  hostId,
  isHost,
  users,
  localStream,
  remoteStream,
  isConnected,
  isMuted,
  isSpeakerOn,
  error,
  isMembersOpen,
  onCloseMembers,
  onToggleMute,
  onToggleSpeaker,
  onOpenMembers,
  onOpenInvite,
  onOpenSettings,
  onLeave,
}: VoiceRoomPageProps) {
  const members = useMemo(
    () => buildMembers({ users, username, hostId, isHost, isMuted, isConnected, remoteStream }),
    [users, username, hostId, isHost, isMuted, isConnected, remoteStream],
  )
  const onlineMemberCount = members.filter(member => member.isOnline).length
  const roomStatus = getRoomStatus(isConnected, localStream, error)
  const hostName = members.find(member => member.role === 'host' && member.isOnline)?.name || username

  const activities: RoomActivityItem[] = [
    {
      id: 'local-ready',
      title: localStream ? '你在房间里' : '正在准备麦克风',
      detail: localStream ? '等朋友进来，就能直接聊' : '允许后，就能开口聊',
    },
    {
      id: 'connection-state',
      title: roomStatus.connectionText,
      detail: roomStatus.qualityText,
    },
    {
      id: 'mute-state',
      title: isMuted ? '你已静音' : '可以说话',
      detail: isMuted ? '房间暂时听不到你' : '想安静一下，点麦克风就好',
    },
  ]

  return (
    <main className="voice-room-page">
      <RemoteAudio stream={remoteStream} isSpeakerOn={isSpeakerOn} />

      <RoomTopBar
        roomId={roomId}
        hostName={hostName}
        memberCount={onlineMemberCount}
        status={roomStatus}
        onInvite={onOpenInvite}
      />

      <div className="room-layout">
        <MemberSeatGrid members={members} onInvite={onOpenInvite} />

        <div className="room-side-column">
          <MemberPanel members={members} isOpen={isMembersOpen} onClose={onCloseMembers} />
          <RoomActivity activities={activities} />
        </div>
      </div>

      {error && <div className="room-error">{error}</div>}

      <ControlDock
        isMuted={isMuted}
        isSpeakerOn={isSpeakerOn}
        onToggleMute={onToggleMute}
        onToggleSpeaker={onToggleSpeaker}
        onOpenMembers={onOpenMembers}
        onOpenInvite={onOpenInvite}
        onOpenSettings={onOpenSettings}
        onLeave={onLeave}
      />
    </main>
  )
}
