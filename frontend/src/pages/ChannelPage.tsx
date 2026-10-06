import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import BackHeader from '../components/layout/BackHeader'
import HostTransferModal from '../components/modals/HostTransferModal'
import ControlDock from '../components/room/ControlDock'
import MemberSeatGrid from '../components/room/MemberSeatGrid'
import MemberSheet from '../components/room/MemberSheet'
import RaiseSheet from '../components/room/RaiseSheet'
import RemoteAudio from '../components/room/RemoteAudio'
import type { User } from '../hooks/useVoiceRoom'
import { useVoiceRoom } from '../hooks/useVoiceRoom'
import { clearSession, getAuthUser } from '../services/auth'
import { recordRoomVisit } from '../services/history'
import type { AIAssistantState } from '../types/signaling'
import type { RoomParticipantRole, RoomStatusCopy, VoiceRoomMember } from '../types/voiceRoomUi'

// BuildMembersInput 汇总 buildMembers 需要的语音房状态，避免传参过长
interface BuildMembersInput {
  users: User[] // 实时成员快照（含角色）
  selfId: string | null // 当前登录用户 ID
  username: string // 当前用户昵称
  hostId: string | null // 房主 ID
  isMuted: boolean // 本地麦克风静音
  isConnected: boolean // 音频连接是否就绪
  remoteStreams: Map<string, MediaStream> // 远端音频流
  waitingUserIds: Set<string> // 举手等待集合
  mutedUserIds: Set<string> // 管理静音集合
  aiState: AIAssistantState // AI 三态
}

// buildMembers 把 hook 状态合成为席位需要的成员视图：
//   - self 一律按 user_id 判定，不用昵称匹配
//   - muted/waiting 是独立快速状态的叠加展示，self 的静音还要并入本地麦克风开关
function buildMembers(input: BuildMembersInput): VoiceRoomMember[] {
  const {
    users,
    selfId,
    username,
    hostId,
    isMuted,
    isConnected,
    remoteStreams,
    waitingUserIds,
    mutedUserIds,
    aiState,
  } = input

  // self 的合成静音 = 本地麦克风开关 OR 被管理静音，他人只取被管理静音
  const selfMuted = isMuted || (selfId !== null && mutedUserIds.has(selfId))

  const realMembers: VoiceRoomMember[] = users.map(user => ({
    id: user.id,
    name: user.username,
    role: user.role as RoomParticipantRole,
    isSelf: user.id === selfId,
    isMuted: user.id === selfId ? selfMuted : mutedUserIds.has(user.id),
    isSpeaking: user.id === selfId ? !selfMuted && isConnected : user.isSpeaking,
    isOnline: true,
    hasAudio: remoteStreams.has(user.id),
    isWaiting: waitingUserIds.has(user.id),
  }))

  // 自己还没进入快照（加入成功前的首个信令未到）时补一个本地占位，避免空屏闪烁
  const hasSelf = selfId !== null && realMembers.some(member => member.isSelf)
  if (!hasSelf) {
    realMembers.unshift({
      id: 'local-user',
      name: username || '我',
      role: hostId === selfId ? 'host' : 'listener',
      isSelf: true,
      isMuted: selfMuted,
      isSpeaking: !selfMuted && isConnected,
      isOnline: true,
      hasAudio: false,
      isWaiting: selfId !== null && waitingUserIds.has(selfId),
    })
  }

  // 远端音频已到但成员信令未同步（SFU 先于成员广播到达）时兜底补一个"朋友"席位
  if (remoteStreams.size > 0 && realMembers.length === 1 && realMembers[0]?.isSelf) {
    remoteStreams.forEach((_stream, userId) => {
      // AI 合成语音流（key 为 ai_<clientID>）由固定的 AI 席位展示，跳过避免重复
      if (userId.startsWith('ai_')) return

      realMembers.push({
        id: userId,
        name: '朋友',
        role: 'listener',
        isSelf: false,
        isMuted: false,
        isSpeaking: true,
        isOnline: true,
        hasAudio: true,
      })
    })
  }

  // AI 助手席位是系统固定虚拟成员，始终展示，三态由光环特效表达
  const aiSeat: VoiceRoomMember = {
    id: 'ai-assistant',
    name: '小月',
    role: 'ai',
    isSelf: false,
    isMuted: false,
    isSpeaking: false,
    isOnline: aiState !== 'offline',
    hasAudio: false,
    aiState,
  }

  // 始终保留一个空席位作为邀请入口，点了复制频道链接
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

  return [...realMembers, aiSeat, emptySeat]
}

// getRoomStatus 根据连接状态返回顶部状态胶囊文案
function getRoomStatus(isConnected: boolean, isReconnecting: boolean, error: string | null): RoomStatusCopy {
  if (isReconnecting) return { connectionText: '重连中', qualityText: '正在恢复', tone: 'reconnecting' }
  if (error) return { connectionText: '连接异常', qualityText: '请检查网络', tone: 'reconnecting' }
  if (isConnected) return { connectionText: '连接成功', qualityText: '声音流畅', tone: 'ready' }
  return { connectionText: '准备中', qualityText: '等朋友进来', tone: 'waiting' }
}

// ChannelPage 频道二级页：进入即加入频道，成员管理信令接真实后端
// 移动端布局：sub-top 返回栏 + 频道号 chip + 状态胶囊 + 席位网格 + 底部控制栏 + 成员面板
export default function ChannelPage() {
  const { roomId } = useParams<{ roomId: string }>()
  const normalizedRoomId = roomId?.toUpperCase() || ''
  const navigate = useNavigate()
  const user = getAuthUser()
  const username = user?.displayName || user?.username || ''

  const [activeSheet, setActiveSheet] = useState<'raise' | 'members' | null>(null) // 当前展开的底部面板
  const [isHostTransferOpen, setIsHostTransferOpen] = useState(false) // 房主交接弹窗开关
  const [micToast, setMicToast] = useState<string | null>(null) // 无麦克风等操作提示，短暂展示后自动消失

  const voiceRoom = useVoiceRoom({
    onKicked: () => {
      // 被服务端踢下线意味着会话已死（吊销/改密/封禁）：清本地会话并回登录页
      // 原实现只跳 /，会停在"看似已登录"的首页（默认频道 Tab），与本意不符
      setIsHostTransferOpen(false)
      clearSession()
      navigate('/login', { replace: true })
    },
    onAuthExpired: () => {
      // 会话已失效且刷新救不回：清本地会话并直接回登录页
      // 直接跳 /login 而非 /，不依赖路由守卫二次判定，避免清态与守卫时序不一致时停留在房间页
      clearSession()
      navigate('/login', { replace: true })
    },
  })

  // 进入页面即加入频道：房间号来自路由参数，昵称取登录用户展示名
  // 无可用麦克风也照常进房，只是麦克风开关点按会提示未检测到设备
  const joinedRef = useRef(false)
  useEffect(() => {
    if (!normalizedRoomId || joinedRef.current) return
    joinedRef.current = true
    void (async () => {
      await voiceRoom.joinRoom(normalizedRoomId, username)
      // 进房成功即落本地历史，供「我的-历史记录」子页展示与深链直达
      recordRoomVisit(normalizedRoomId)
    })()
  }, [normalizedRoomId, username, voiceRoom])

  // 无麦克风提示短暂展示后自动消失，避免常驻遮挡控制栏
  useEffect(() => {
    if (!micToast) return
    const timer = window.setTimeout(() => setMicToast(null), 2200)
    return () => window.clearTimeout(timer)
  }, [micToast])

  const selfId = voiceRoom.selfId
  // 自己是否在等待上麦，驱动上麦按钮的禁用置灰
  const isWaiting = selfId !== null && voiceRoom.waitingUserIds.has(selfId)

  const members = useMemo(
    () =>
      buildMembers({
        users: voiceRoom.users,
        selfId,
        username,
        hostId: voiceRoom.hostId,
        isMuted: voiceRoom.isMuted,
        isConnected: voiceRoom.isConnected,
        remoteStreams: voiceRoom.remoteStreams,
        waitingUserIds: voiceRoom.waitingUserIds,
        mutedUserIds: voiceRoom.mutedUserIds,
        aiState: voiceRoom.aiState,
      }),
    [
      selfId,
      username,
      voiceRoom.users,
      voiceRoom.hostId,
      voiceRoom.isMuted,
      voiceRoom.isConnected,
      voiceRoom.remoteStreams,
      voiceRoom.waitingUserIds,
      voiceRoom.mutedUserIds,
      voiceRoom.aiState,
    ],
  )

  const onlineCount = members.filter(member => member.isOnline).length
  const status = getRoomStatus(voiceRoom.isConnected, voiceRoom.isReconnecting, voiceRoom.error)

  // 房主离开交接的候选 = 除自己外的在线成员
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

  // 邀请 = 复制频道深链，新窗口可直接 /channel/:roomId 直达
  const handleInvite = useCallback(() => {
    const link = `${window.location.origin}/channel/${normalizedRoomId}`
    navigator.clipboard.writeText(link).catch(() => {})
  }, [normalizedRoomId])

  // 麦克风开关：无可用麦克风时不开麦，只弹提示；有麦克风才真正切换静音
  const handleToggleMute = useCallback(() => {
    if (!voiceRoom.hasMicrophone) {
      setMicToast('未检测到可用麦克风')
      return
    }
    voiceRoom.toggleMute()
  }, [voiceRoom.hasMicrophone, voiceRoom.toggleMute])

  return (
    <main className="voice-room-page">
      {Array.from(voiceRoom.remoteStreams.entries()).map(([userId, stream]) => (
        <RemoteAudio key={userId} stream={stream} isSpeakerOn={voiceRoom.isSpeakerOn} />
      ))}

      <BackHeader ariaLabel="返回主界面" onBack={handleLeave} title={normalizedRoomId} />

      <div className="channel-head">
        <div className="room-chip">
          <span className="no">频道</span>
          <span className="cnt">● {onlineCount} 人在线</span>
        </div>
        <span className={`status-pill ${status.tone}`}>
          <i />
          {status.connectionText}
        </span>
      </div>

      {voiceRoom.isReconnecting && <div className="reconnect-banner">🔄 连接不太稳，正在帮你恢复…</div>}

      {/* 待机态唤醒提示气泡：仅待机时显示，提示用户唤醒词 */}
      {voiceRoom.aiState === 'standby' && (
        <div className="ai-standby-bubble" role="status" aria-label="唤醒提示">
          <span className="asb-ic">✨</span>
          <span className="asb-txt">
            呼唤「<em>小月</em>」唤醒我
          </span>
        </div>
      )}

      <MemberSeatGrid members={members} onInvite={handleInvite} />

      {voiceRoom.error && <div className="room-error">{voiceRoom.error}</div>}

      {micToast && <div className="room-toast" role="status">{micToast}</div>}

      <ControlDock
        isMuted={voiceRoom.isMuted}
        isSpeakerOn={voiceRoom.isSpeakerOn}
        isAIEnabled={voiceRoom.isAIEnabled}
        canManage={voiceRoom.canManage}
        selfRole={voiceRoom.selfRole}
        isWaiting={isWaiting}
        activeSheet={activeSheet}
        onToggleMute={handleToggleMute}
        onToggleSpeaker={voiceRoom.toggleSpeaker}
        onToggleAI={voiceRoom.toggleAI}
        onRaiseHand={voiceRoom.raiseHand}
        onToggleSheet={sheet => setActiveSheet(prev => (prev === sheet ? null : sheet))}
      />

      <RaiseSheet
        isOpen={activeSheet === 'raise'}
        users={voiceRoom.users}
        waitingUserIds={voiceRoom.waitingUserIds}
        selfId={selfId}
        canManage={voiceRoom.canManage}
        onClose={() => setActiveSheet(null)}
        onApprove={voiceRoom.approveMic}
        onReject={voiceRoom.rejectMic}
      />

      <MemberSheet
        isOpen={activeSheet === 'members'}
        users={voiceRoom.users}
        waitingUserIds={voiceRoom.waitingUserIds}
        mutedUserIds={voiceRoom.mutedUserIds}
        selfId={selfId}
        canManage={voiceRoom.canManage}
        onClose={() => setActiveSheet(null)}
        onApprove={voiceRoom.approveMic}
        onReject={voiceRoom.rejectMic}
        onKick={voiceRoom.kickMic}
        onMute={voiceRoom.muteMic}
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