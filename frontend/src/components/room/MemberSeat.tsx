import type { VoiceRoomMember } from '../../types/voiceRoomUi'

interface MemberSeatProps {
  member: VoiceRoomMember
  onInvite: () => void
}

// 按成员 id 取色板索引，确保同一个人颜色稳定
const GRADIENT_CLASSES = ['g1', 'g2', 'g3', 'g4', 'g5', 'g6']
function avatarGradient(id: string) {
  let hash = 0
  for (let i = 0; i < id.length; i++) {
    hash = id.charCodeAt(i) + ((hash << 5) - hash)
  }
  return GRADIENT_CLASSES[Math.abs(hash) % GRADIENT_CLASSES.length]
}

// MemberSeat 渲染单个席位：真实成员展示头像/昵称/状态，空席位作为邀请入口
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
  const statusText = member.isMuted ? '已静音' : member.isSpeaking ? '说话中' : '在线'
  const gradientClass = avatarGradient(member.id)

  return (
    <div className={`seat${isSpeaking ? ' speaking' : ''}${member.isMuted ? ' muted' : ''}`}>
      <div className={`ava ${gradientClass}`}>
        {member.name.slice(0, 1)}
        {member.role === 'host' && <span className="badge-host">👑</span>}
        {member.isMuted && <span className="badge-mute">🔇</span>}
      </div>
      {isSpeaking && (
        <div className="wave">
          <i /><i /><i />
        </div>
      )}
      <div className="nm">{member.isSelf ? `${member.name} · 我` : member.name}</div>
      {!isSpeaking && <div className="st">{statusText}</div>}
    </div>
  )
}
