import type { VoiceRoomMember } from '../../types/voiceRoomUi'
import MemberSeat from './MemberSeat'

interface MemberSeatGridProps {
  members: VoiceRoomMember[]
  onInvite: () => void
}

// MemberSeatGrid 声聊间主舞台，以圆形头像席位展示所有在线成员和空位
export default function MemberSeatGrid({ members, onInvite }: MemberSeatGridProps) {
  return (
    <section className="room-stage" aria-label="成员席位">
      {members.map(member => (
        <MemberSeat key={member.id} member={member} onInvite={onInvite} />
      ))}
    </section>
  )
}
