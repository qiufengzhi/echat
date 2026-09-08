import { useNavigate } from 'react-router-dom'

// SETTING_ROWS 设置子页的占位列表项，后续在独立阶段逐个接入真实功能
const SETTING_ROWS = [
  { icon: '🌙', label: '深色模式', val: '›' },
  { icon: 'ℹ️', label: '关于', val: 'v0.1' },
] as const

// SettingsPage 我的-设置子页：row-item 结构占位列表，仅渲染静态项
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
          <svg
            width="18"
            height="18"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M15 18l-6-6 6-6" />
          </svg>
        </button>
        <h1 className="title">设置</h1>
      </header>

      {SETTING_ROWS.map(row => (
        <div className="row-item" key={row.label}>
          <span className="ri">{row.icon}</span>
          <b>{row.label}</b>
          <span className="val">{row.val}</span>
        </div>
      ))}
    </div>
  )
}