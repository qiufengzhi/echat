import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import BackHeader from '../../components/layout/BackHeader'
import { login } from '../../services/auth'

// LoginPage 登录页：identifier 支持用户名或邮箱 + 密码，成功换双 token 后进主界面
export default function LoginPage() {
  const navigate = useNavigate()
  const [identifier, setIdentifier] = useState('') // 登录标识（用户名或邮箱）
  const [password, setPassword] = useState('') // 密码
  const [loggingIn, setLoggingIn] = useState(false) // 请求中置灰按钮
  const [error, setError] = useState<string | null>(null) // 登录失败提示

  // handleSubmit 提交登录，成功保存会话并跳主界面，失败展示后端泛化文案
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!identifier.trim() || !password) {
      setError('输入账号和密码再登录')
      return
    }
    setLoggingIn(true)
    setError(null)
    try {
      await login(identifier.trim(), password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败，请稍后重试')
    } finally {
      setLoggingIn(false)
    }
  }

  // hasHistory 是否有上一页可退：登录页是未登录流入口，直链打开时无返回目标，不渲染返回栏
  const hasHistory = window.history.length > 1

  return (
    <div className="auth-page">
      {hasHistory && <BackHeader onBack={() => navigate(-1)} />}
      <main className="auth-body">
        <div className="h1">欢迎回来</div>

        <form className="auth-form" onSubmit={handleSubmit} noValidate>
          {error && (
            <div className="form-message" role="alert">
              ⚠️ {error}
            </div>
          )}

          <label className="field">
            <span>邮箱</span>
            <input
              type="text"
              value={identifier}
              onChange={e => { setIdentifier(e.target.value); setError(null) }}
              placeholder="用户名或邮箱"
              autoComplete="username"
              disabled={loggingIn}
            />
          </label>

          <label className="field">
            <span className="field-row">
              密码
              <Link to="/forgot" className="field-link">
                忘记密码?
              </Link>
            </span>
            <input
              type="password"
              value={password}
              onChange={e => { setPassword(e.target.value); setError(null) }}
              placeholder="至少 8 位"
              autoComplete="current-password"
              disabled={loggingIn}
            />
          </label>

          <button className="primary-button" type="submit" disabled={loggingIn}>
            {loggingIn ? '登录中…' : '登 录'}
          </button>
        </form>

        <div className="divider">还没有账号?</div>
        <Link className="secondary-button auth-switch" to="/register">
          去注册
        </Link>
      </main>
    </div>
  )
}
