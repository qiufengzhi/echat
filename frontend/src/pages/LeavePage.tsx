import type { LeaveRoomSummary } from '../types/voiceRoomUi'

interface LeavePageProps {
  summary: LeaveRoomSummary | null // 用户刚刚离开的房间摘要，为空时展示通用离开态
  onRejoin: () => void // 重新加入上一次房间
  onHome: () => void // 回到首页并重新开始
}

// LeavePage 给离开动作一个明确收尾，让用户知道麦克风和房间连接都已经关闭
export default function LeavePage({ summary, onRejoin, onHome }: LeavePageProps) {
  return (
    <main className="leave-page">
      <section className="leave-main">
        <div className="leave-icon" aria-hidden="true">
          ✓
        </div>
        <p className="eyebrow">已离开</p>
        <h1>你已离开房间</h1>
        <p>
          这次聊天先到这里，麦克风已关闭
        </p>

        <div className="leave-actions">
          <button className="primary-button" type="button" onClick={onRejoin} disabled={!summary}>
            重新加入
          </button>
          <button className="secondary-button" type="button" onClick={onHome}>
            回到首页
          </button>
        </div>
      </section>

      <aside className="leave-summary" aria-label="本次房间摘要">
        <div>
          <span>房间号</span>
          <strong>{summary?.roomId || '--'}</strong>
        </div>
        <div>
          <span>昵称</span>
          <strong>{summary?.username || '--'}</strong>
        </div>
        <div>
          <span>停留时间</span>
          <strong>{summary?.durationText || '刚刚'}</strong>
        </div>
        <div>
          <span>成员</span>
          <strong>{summary ? `${summary.memberCount} 人` : '--'}</strong>
        </div>
      </aside>
    </main>
  )
}
