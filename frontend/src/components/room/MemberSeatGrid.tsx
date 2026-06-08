import type { VoiceRoomMember } from '../../types/voiceRoomUi'
import MemberSeat from './MemberSeat'

interface MemberSeatGridProps {
  members: VoiceRoomMember[] // 声聊间席位列表，包含真实成员和空席位。
  onInvite: () => void // 点击空席位时打开邀请弹窗。
}

// MemberSeatGrid 是声聊间主视觉区域，让页面从二人调试视角切换成多人席位视角。
export default function MemberSeatGrid({ members, onInvite }: MemberSeatGridProps) {
  return (
    <section className="room-stage" aria-labelledby="member-seat-title">
      <div className="stage-heading">
        <div>
          <p className="eyebrow">声聊间</p>
          <h2 id="member-seat-title">成员席位</h2>
        </div>
      </div>

      <div className="member-seat-grid">
        {members.map(member => (
          <MemberSeat key={member.id} member={member} onInvite={onInvite} />
        ))}
      </div>
    </section>
  )
}
