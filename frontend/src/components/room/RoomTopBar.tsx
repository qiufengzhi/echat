import type { RoomStatusCopy } from '../../types/voiceRoomUi'

interface RoomTopBarProps {
  roomId: string // 当前房间号
  hostName: string // 房主昵称
  memberCount: number // 当前在线成员数量
  status: RoomStatusCopy // 用户可见的连接和声音状态
  onInvite: () => void // 打开邀请弹窗
}

// RoomTopBar 展示房间的核心上下文，让用户随时知道自己在哪个房间、连接是否正常
export default function RoomTopBar({ roomId, hostName, memberCount, status, onInvite }: RoomTopBarProps) {
  return (
    <header className="room-topbar">
      <div className="room-title">
        <span>声聊间</span>
        <h1>房间 {roomId}</h1>
      </div>

      <div className="room-meta" aria-label="房间信息">
        <span>{memberCount} 人在线</span>
        <span>房主 {hostName || '我'}</span>
      </div>

      <div className="room-statuses" aria-label="房间状态">
        <span className={`status-pill ${status.tone}`}>
          <i />
          {status.connectionText}
        </span>
        <span className={`status-pill ${status.tone}`}>{status.qualityText}</span>
      </div>

      <button className="primary-button compact" type="button" onClick={onInvite}>
        ＋ 邀请
      </button>
    </header>
  )
}
