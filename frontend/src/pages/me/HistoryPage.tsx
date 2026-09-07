import { useNavigate } from 'react-router-dom'

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
      <header className="sub-top">
        <button className="back" type="button" aria-label="返回" onClick={handleBack}>
          ←
        </button>
        <h1 className="title">历史记录</h1>
      </header>

      {entries.length === 0 ? (
        <div className="empty-state">
          <span className="es-ic">🕘</span>
          <b>还没有进过频道</b>
          <p>在主界面创建或加入频道后，记录会出现在这里</p>
        </div>
      ) : (
        <ul className="history-list">
          {entries.map(entry => (
            <li key={entry.roomId}>
              <button type="button" onClick={() => navigate(`/channel/${entry.roomId}`)}>
                <div className="hl-main">
                  <b>{entry.roomId}</b>
                  <span>{formatTime(entry.joinedAt)}</span>
                </div>
                <span className="chev">›</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}