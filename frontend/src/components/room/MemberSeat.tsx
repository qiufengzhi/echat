import type { VoiceRoomMember } from '../../types/voiceRoomUi'

interface MemberSeatProps {
  member: VoiceRoomMember // 当前席位展示的成员或空席位。
  onInvite: () => void // 点击空席位时打开邀请弹窗。
}

// MemberSeat 渲染单个成员席位：真实成员展示昵称/状态，空席位展示邀请入口。
export default function MemberSeat({ member, onInvite }: MemberSeatProps) {
  if (member.role === 'empty') {
    return (
      <button className="member-seat member-seat-empty" type="button" onClick={onInvite}>
        <span className="seat-avatar">＋</span>
        <strong>邀请好友</strong>
      </button>
    )
  }

  const statusText = member.isMuted ? '已静音' : member.isSpeaking ? '正在说话' : '在线'

  return (
    <article className={`member-seat ${member.isSpeaking ? 'is-speaking' : ''} ${member.isMuted ? 'is-muted' : ''}`}>
      <div className="seat-avatar" aria-hidden="true">
        {member.name.slice(0, 1).toUpperCase()}
      </div>
      <div className="seat-copy">
        <strong>{member.isSelf ? `${member.name} · 我` : member.name}</strong>
        <span>{member.role === 'host' ? '房主' : '成员'}</span>
      </div>
      <small>{statusText}</small>
    </article>
  )
}
