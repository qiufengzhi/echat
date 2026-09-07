import { useNavigate } from 'react-router-dom'

import { getAuthUser, logout } from '../../services/auth'

// MeTab 是主界面「我的」页：登录用户真实资料卡 + 菜单占位 + 退出登录
// 历史记录/设置子页在阶段 4 落地并接入路由，此处仅渲染菜单结构
export default function MeTab() {
  const navigate = useNavigate()
  const user = getAuthUser()
  const name = user?.display_name || user?.username || '我'
  const initial = name.trim().slice(0, 1) || '我'

  const handleLogout = () => {
    logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="me-body">
      <div className="me-cover">
        <div className="me-ava">{initial}</div>
      </div>

      <div className="me-info">
        <div className="row">
          <b>{name}</b>
        </div>
        <div className="id">{user ? `@${user.username}` : ''}</div>
      </div>

      <div className="me-menu">
        <div className="item">
          <span className="mi">🕘</span>
          <b>历史记录</b>
          <span className="chev">›</span>
        </div>
        <div className="item">
          <span className="mi">⚙️</span>
          <b>设置</b>
          <span className="chev">›</span>
        </div>
      </div>

      <div className="logout-btn">
        <button type="button" onClick={handleLogout}>
          退出登录
        </button>
      </div>
    </div>
  )
}