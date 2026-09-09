import { useNavigate } from 'react-router-dom'

import BackHeader from '../../components/layout/BackHeader'
import { getRoomHistory } from '../../services/history'

// formatTime 把时间戳格式化为本地日期时间，避免依赖运行环境时区假设
function formatTime(timestamp: number): string {
  const d = new Date(timestamp)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// HistoryPage 我的-历史记录子页：列出本地进出的频道记录，点击可深链直达频道
export default function HistoryPage() {
  const navigate = useNavigate()
  const entries = getRoomHistory()

  const handleBack = () => {
    // 从主界面点进来的用浏览器回退，直链打开时才退回主界面
    if (window.history.length > 1) navigate(-1)
    else navigate('/')
  }

  return (
    <div className="me-sub">
      <BackHeader title="历史记录" onBack={handleBack} />

      {entries.length === 0 ? (
        <div className="empty-state">
          <span className="es-ic">🕘</span>
          <b>还没有进过频道</b>
        </div>
      ) : (
        entries.map(entry => (
          <button
            type="button"
            className="row-item"
            key={entry.roomId}
            onClick={() => navigate(`/channel/${entry.roomId}`)}
          >
            <span className="ri">🎤</span>
            <b>{entry.roomId}</b>
            <span className="val">{formatTime(entry.joinedAt)}</span>
          </button>
        ))
      )}
    </div>
  )
}