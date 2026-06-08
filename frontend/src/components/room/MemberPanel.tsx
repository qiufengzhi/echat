import type { VoiceRoomMember } from '../../types/voiceRoomUi'

interface MemberPanelProps {
  members: VoiceRoomMember[] // 当前在线成员和页面展示席位。
  isOpen: boolean // 手机端成员抽屉是否打开。
  onClose: () => void // 关闭手机端成员抽屉。
}

// MemberPanel 在桌面端作为右侧栏，在手机端作为抽屉展示成员状态。
export default function MemberPanel({ members, isOpen, onClose }: MemberPanelProps) {
  const onlineMembers = members.filter(member => member.isOnline)

  return (
    <aside className={`room-side ${isOpen ? 'is-open' : ''}`} aria-label="成员列表">
      <div className="side-card">
        <div className="side-heading">
          <h2>成员</h2>
          <span>{onlineMembers.length} 在线</span>
          <button className="close-button mobile-only" type="button" aria-label="关闭成员列表" onClick={onClose}>
            ×
          </button>
        </div>

        <div className="member-list">
          {onlineMembers.map(member => (
            <div className="member-row" key={member.id}>
              <span className={`member-dot ${member.isSpeaking ? 'speaking' : ''}`} />
              <div>
                <strong>{member.isSelf ? `${member.name} · 我` : member.name}</strong>
                <em>{member.role === 'host' ? '房主' : member.isMuted ? '已静音' : '在线'}</em>
              </div>
            </div>
          ))}
        </div>
      </div>
    </aside>
  )
}
