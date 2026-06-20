import { useMemo } from 'react'

import ControlDock from '../components/room/ControlDock'
import MemberPanel from '../components/room/MemberPanel'
import MemberSeatGrid from '../components/room/MemberSeatGrid'
import RemoteAudio from '../components/room/RemoteAudio'
import RoomActivity from '../components/room/RoomActivity'
import RoomTopBar from '../components/room/RoomTopBar'
import type { RoomActivityItem, RoomStatusCopy, VoiceRoomMember } from '../types/voiceRoomUi'

// HookUser 是 useVoiceRoom 输出的后端成员状态，页面会再转换成更适合席位展示的 VoiceRoomMember
interface HookUser {
  id: string // 后端生成的成员 ID
  username: string // 成员昵称
  isMuted: boolean // 该成员是否静音
  isSpeaking: boolean // 该成员是否正在说话
}

// VoiceRoomPageProps 汇总声聊间页面需要的实时状态和用户操作
interface VoiceRoomPageProps {
  roomId: string // 当前房间号
  username: string // 当前用户昵称，用于本地兜底席位和离开摘要
  hostId: string | null // 服务端返回的当前房主 ID，null 表示还没收到房主状态
  isHost: boolean // 当前用户是否为房主，用于本地兜底席位和离开交接入口
  users: HookUser[] // useVoiceRoom 提供的真实成员列表
  localStream: MediaStream | null // 本地麦克风音频流
  remoteStreams: Map<string, MediaStream> // SFU 架构下多个远端用户的音频流，key 为用户 ID
  isConnected: boolean // 声音连接是否已经成功建立
  isMuted: boolean // 当前用户是否静音
  isSpeakerOn: boolean // 当前是否播放房间声音
  error: string | null // 需要展示给用户的错误提示
  isMembersOpen: boolean // 手机端成员抽屉是否打开
  onCloseMembers: () => void // 关闭手机端成员抽屉
  onToggleMute: () => void // 切换麦克风静音
  onToggleSpeaker: () => void // 切换房间声音播放
  onOpenMembers: () => void // 打开成员面板
  onOpenInvite: () => void // 打开邀请弹窗
  onOpenSettings: () => void // 打开设置弹窗
  onLeave: () => void // 离开房间；房主离开时 App 会先处理交接
}

const MAX_VISIBLE_SEATS = 8

// buildMembers 把真实连接状态和远端音频流整理成席位列表
function buildMembers({
  users,
  username,
  hostId,
  isHost,
  isMuted,
  isConnected,
  remoteStreams,
}: Pick<
  VoiceRoomPageProps,
  'users' | 'username' | 'hostId' | 'isHost' | 'isMuted' | 'isConnected' | 'remoteStreams'
>) {
  const knownUsers = users.length > 0 ? users : []
  // 成员同步依赖后端 user_id，但初次等待时可能还没有完整列表，所以需要检查本地用户是否已出现
  const hasSelfInUsers = knownUsers.some(user => user.username === username)

  const realMembers: VoiceRoomMember[] = knownUsers.map(user => ({
    id: user.id,
    name: user.username,
    role: user.id === hostId ? 'host' : 'member',
    isSelf: user.username === username,
    isMuted: user.username === username ? isMuted : user.isMuted,
    isSpeaking: user.username === username ? !isMuted && Boolean(isConnected) : user.isSpeaking,
    isOnline: true,
    // SFU 下每个远端成员有独立音频流，通过 user.id 检查是否已有流
    hasAudio: remoteStreams.has(user.id),
  }))

  if (!hasSelfInUsers) {
    // 加入成功但还没收到服务端成员快照时，用本地信息先展示"我"的席位
    realMembers.unshift({
      id: 'local-user',
      name: username || '我',
      role: isHost ? 'host' : 'member',
      isSelf: true,
      isMuted,
      isSpeaking: !isMuted && Boolean(isConnected),
      isOnline: true,
      hasAudio: false,
    })
  }

  if (remoteStreams.size > 0 && realMembers.length === 1 && realMembers[0]?.isSelf) {
    // 有远端音频流但成员列表还没同步时，为每个远端流添加一个"朋友"席位
    // 正常情况应当由服务端 user_joined 同步成员列表，此分支只做兜底
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

  // 空席位不是在线成员，只作为邀请入口和多人房视觉占位
  const emptySeatCount = Math.max(0, MAX_VISIBLE_SEATS - realMembers.length)
  const emptySeats: VoiceRoomMember[] = Array.from({ length: emptySeatCount }, (_, index) => ({
    id: `empty-seat-${index + 1}`,
    name: '空席位',
    role: 'empty',
    isSelf: false,
    isMuted: false,
    isSpeaking: false,
    isOnline: false,
    hasAudio: false,
  }))

  return [...realMembers, ...emptySeats]
}

// getRoomStatus 把底层连接状态映射成用户能理解的自然语言，不在页面暴露技术词
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

// VoiceRoomPage 组合声聊间的完整体验：顶部信息、席位、成员/动态、底部控制和隐藏音频播放器
export default function VoiceRoomPage({
  roomId,
  username,
  hostId,
  isHost,
  users,
  localStream,
  remoteStreams,
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
  // members 是页面组件真正消费的席位模型，包含真实成员、兜底成员和空席位
  const members = useMemo(
    () => buildMembers({ users, username, hostId, isHost, isMuted, isConnected, remoteStreams }),
    [users, username, hostId, isHost, isMuted, isConnected, remoteStreams],
  )
  const onlineMemberCount = members.filter(member => member.isOnline).length
  const roomStatus = getRoomStatus(isConnected, localStream, error)
  // 顶部房主名直接从席位里找，确保房主标记和顶部信息使用同一份转换结果
  const hostName = members.find(member => member.role === 'host' && member.isOnline)?.name || username

  // 房间动态由页面根据当前连接和控制状态生成，后续可替换为服务端事件流
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
      {/* SFU 架构下，为每个远端用户渲染一个隐藏音频播放器 */}
      {Array.from(remoteStreams.entries()).map(([userId, stream]) => (
        <RemoteAudio key={userId} stream={stream} isSpeakerOn={isSpeakerOn} />
      ))}

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
