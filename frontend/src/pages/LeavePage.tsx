import type { LeaveRoomSummary } from '../types/voiceRoomUi'

interface LeavePageProps {
  summary: LeaveRoomSummary | null
  onRejoin: () => void
  onHome: () => void
}

export default function LeavePage({ summary, onRejoin, onHome }: LeavePageProps) {
  return (
    <main className="leave-page">
      <div className="leave-main">
        <div className="leave-icon" aria-hidden="true">✓</div>
        <h1>已离开房间</h1>
        <p className="sub">麦克风已关闭，这次聊天先到这里</p>

        <div className="leave-summary">
          <div className="sum-cell">
            <span>房间号</span>
            <b>{summary?.roomId || '--'}</b>
          </div>
          <div className="sum-cell">
            <span>昵称</span>
            <b>{summary?.username || '--'}</b>
          </div>
          <div className="sum-cell">
            <span>停留时长</span>
            <b>{summary?.durationText || '刚刚'}</b>
          </div>
          <div className="sum-cell">
            <span>成员</span>
            <b>{summary ? `${summary.memberCount} 人` : '--'}</b>
          </div>
        </div>

        <div className="leave-actions">
          <button className="primary-button" type="button" onClick={onRejoin} disabled={!summary}>
            重新加入
          </button>
          <button className="secondary-button" type="button" onClick={onHome}>
            回到首页
          </button>
        </div>
      </div>
    </main>
  )
}
