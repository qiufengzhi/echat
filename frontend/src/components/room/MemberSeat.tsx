import type { VoiceRoomMember } from '../../types/voiceRoomUi'

interface MemberSeatProps {
  member: VoiceRoomMember
  onInvite: () => void
}

// 按成员 id 取色板索引，确保同一个人颜色稳定
const GRADIENT_CLASSES = ['g1', 'g2', 'g3', 'g4', 'g5', 'g6']

// avatarGradient 根据成员 ID 计算稳定的渐变头像色板类名，供席位与成员面板共用
export function avatarGradient(id: string) {
  let hash = 0
  for (let i = 0; i < id.length; i++) {
    hash = id.charCodeAt(i) + ((hash << 5) - hash)
  }
  return GRADIENT_CLASSES[Math.abs(hash) % GRADIENT_CLASSES.length]
}

// SeatRole 席位可带徽章的角色，排除 AI 与空席位
type SeatRole = 'host' | 'cohost' | 'speaker' | 'listener'

// ROLE_BADGE 席位角色徽标全词文案，配色见 .badge-role 各变体
const ROLE_BADGE: Record<SeatRole, string> = {
  host: '房主', // 房主，创建房间的人
  cohost: '副房主', // 副房主，继承管理权限
  speaker: '嘉宾', // 上麦嘉宾
  listener: '听众', // 听众，可举手
}

// MemberSeat 渲染单个席位：真实成员展示头像/角色徽章/昵称/状态副文案，空席位作为邀请入口
// 状态文案优先级：已静音 > 举手了 > 正在说话 > 已连麦 > 在线
export default function MemberSeat({ member, onInvite }: MemberSeatProps) {
  if (member.role === 'empty') {
    return (
      <button className="seat empty" type="button" onClick={onInvite}>
        <div className="ava">＋</div>
        <div className="nm">邀请</div>
      </button>
    )
  }

  if (member.role === 'ai') {
    // AI 助手席位：三态全部用光环特效表达，不显示状态文字
    const aiState = member.aiState ?? 'offline'
    return (
      <div className={`seat ai ${aiState}`}>
        <div className="ava">
          <span className="ai-ic">🤖</span>
        </div>
        <div className="nm">{member.name}</div>
      </div>
    )
  }

  const isSpeaking = member.isSpeaking && !member.isMuted
  const statusText = member.isMuted
    ? '已静音'
    : member.isWaiting
      ? '举手了'
      : isSpeaking
        ? '正在说话'
        : member.role === 'speaker' || member.role === 'cohost'
          ? '已连麦'
          : '在线'
  const gradientClass = avatarGradient(member.id)

  return (
    <div className={`seat${isSpeaking ? ' speaking' : ''}`}>
      <div className={`ava ${gradientClass}`}>
        {member.name.slice(0, 1)}
        <span className={`badge-role ${member.role}`}>{ROLE_BADGE[member.role]}</span>
        {member.isWaiting && <span className="raise-ic">🙌</span>}
      </div>
      <div className="nm">{member.isSelf ? `${member.name} · 我` : member.name}</div>
      <div className="sg">{statusText}</div>
    </div>
  )
}