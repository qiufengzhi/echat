import { useNavigate } from 'react-router-dom'

import BackHeader from '../../components/layout/BackHeader'

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
      <BackHeader title="设置" onBack={handleBack} />

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