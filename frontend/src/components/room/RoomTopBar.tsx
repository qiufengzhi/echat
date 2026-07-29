import type { RoomStatusCopy } from '../../types/voiceRoomUi'

interface RoomTopBarProps {
  roomId: string
  memberCount: number
  status: RoomStatusCopy
}

// RoomTopBar 展示房间号和人数，仅在断连时显示状态提示
export default function RoomTopBar({ roomId, memberCount, status }: RoomTopBarProps) {
  return (
    <header className="room-topbar">
      <div className="room-chip">
        <span className="no">{roomId}</span>
        {status.tone === 'reconnecting' && (
          <span className="status-pill reconnecting">
            <i />
            {status.connectionText}
          </span>
        )}
        <span className="cnt">{memberCount} 人在线</span>
      </div>
    </header>
  )
}
