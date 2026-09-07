import { useNavigate } from 'react-router-dom'

// SETTING_ROWS 设置子页的占位列表项，后续在独立阶段逐个接入真实功能
const SETTING_ROWS = [
  { icon: '🔔', label: '通知', note: '频道提醒与举手提示' },
  { icon: '🔊', label: '音量', note: '调节语音频道输出音量' },
  { icon: 'ℹ️', label: '关于', note: '苍月草 v0.1' },
] as const

// SettingsPage 我的-设置子页：结构化占位列表，仅渲染静态项
export default function SettingsPage() {
  const navigate = useNavigate()

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
        <h1 className="title">设置</h1>
      </header>

      <ul className="settings-list">
        {SETTING_ROWS.map(row => (
          <li key={row.label}>
            <span className="mi">{row.icon}</span>
            <div className="sl-main">
              <b>{row.label}</b>
              <span>{row.note}</span>
            </div>
            <span className="chev">›</span>
          </li>
        ))}
      </ul>
    </div>
  )
}