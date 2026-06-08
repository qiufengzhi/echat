import { useMemo } from 'react'

import ControlDock from '../components/room/ControlDock'
import MemberPanel from '../components/room/MemberPanel'
import MemberSeatGrid from '../components/room/MemberSeatGrid'
import RemoteAudio from '../components/room/RemoteAudio'
import RoomActivity from '../components/room/RoomActivity'
import RoomTopBar from '../components/room/RoomTopBar'
import type { RoomActivityItem, RoomStatusCopy, VoiceRoomMember } from '../types/voiceRoomUi'

interface HookUser {
  id: string // useVoiceRoom 同步出的后端用户 ID。
  username: string // useVoiceRoom 同步出的成员昵称。
  isMuted: boolean // 该成员是否静音。
  isSpeaking: boolean // 该成员是否正在说话。
}

interface VoiceRoomPageProps {
  roomId: string // 当前房间号。
  username: string // 当前用户昵称。
  isHost: boolean // 当前用户是否为房主。
  users: HookUser[] // useVoiceRoom 提供的真实成员列表。
  localStream: MediaStream | null // 本地麦克风音频流。
  remoteStream: MediaStream | null // 当前远端音频流。
  isConnected: boolean // 声音连接是否已经成功建立。
  isMuted: boolean // 当前用户是否静音。
  isSpeakerOn: boolean // 当前是否播放房间声音。
  error: string | null // 需要展示给用户的错误提示。
  isMembersOpen: boolean // 手机端成员抽屉是否打开。
  onCloseMembers: () => void // 关闭手机端成员抽屉。
  onToggleMute: () => void // 切换麦克风静音。
  onToggleSpeaker: () => void // 切换房间声音播放。
  onOpenMembers: () => void // 打开成员面板。
  onOpenInvite: () => void // 打开邀请弹窗。
  onOpenSettings: () => void // 打开设置弹窗。
  onLeave: () => void // 离开房间。
}

const MAX_VISIBLE_SEATS = 8

// buildMembers 把真实连接状态整理成席位列表；当前底层仍是单远端流，因此不伪造多人音频。
function buildMembers({
  users,
  username,
  isHost,
  isMuted,
  isConnected,
  remoteStream,
}: Pick<VoiceRoomPageProps, 'users' | 'username' | 'isHost' | 'isMuted' | 'isConnected' | 'remoteStream'>) {
  const knownUsers = users.length > 0 ? users : []
  const hasSelfInUsers = knownUsers.some(user => user.username === username)

  const realMembers: VoiceRoomMember[] = knownUsers.map((user, index) => ({
    id: user.id,
    name: user.username,
    role: index === 0 ? 'host' : 'member',
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

// getRoomStatus 把底层连接状态映射成用户能理解的自然语言，不在页面暴露技术词。
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

// VoiceRoomPage 组合声聊间的完整体验：顶部信息、席位、成员/动态、底部控制和隐藏音频播放器。
export default function VoiceRoomPage({
  roomId,
  username,
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
    () => buildMembers({ users, username, isHost, isMuted, isConnected, remoteStream }),
    [users, username, isHost, isMuted, isConnected, remoteStream],
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
