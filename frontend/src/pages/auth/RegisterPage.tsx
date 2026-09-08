import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import { login, register } from '../../services/auth'

// RegisterPage 注册页：昵称(username)/邮箱(可选)/密码；本地账号注册即自动登录进主界面，
// 填了邮箱则 need_verify，提示去邮箱验证后停在此页（后端未投递邮件，见 docs 待实现清单）
export default function RegisterPage() {
  const navigate = useNavigate()
  const [username, setUsername] = useState('') // 昵称（落 username 唯一句柄）
  const [email, setEmail] = useState('') // 可选邮箱
  const [password, setPassword] = useState('') // 密码
  const [submitting, setSubmitting] = useState(false) // 请求中置灰按钮
  const [error, setError] = useState<string | null>(null) // 提交失败提示
  const [verifyTip, setVerifyTip] = useState(false) // 是否停在"请验证邮箱"提示态

  // handleSubmit 注册；注册响应不签 token，本地账号再补一次 login(username,password) 实现自动登录
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!username.trim()) {
      setError('取个昵称吧')
      return
    }
    if (!password || password.length < 8) {
      setError('密码至少 8 位')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const result = await register({
        username: username.trim(),
        password,
        ...(email.trim() ? { email: email.trim() } : {}),
      })
      if (result.need_verify) {
        setVerifyTip(true)
        return
      }
      await login(username.trim(), password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : '注册失败，请稍后重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="auth-body">
      <div className="wordmark auth-logo">
        <span className="d" />
        苍月草
      </div>

      {verifyTip ? (
        <>
          <div className="h1">就差最后一步</div>
          <p className="slogan">验证链接已发到你的邮箱，点一下即可开始使用。</p>
          <Link className="primary-button auth-mt" to="/login">
            返回登录
          </Link>
        </>
      ) : (
        <>
          <div className="h1">给朋友留一个有声音的小频道</div>
          <p className="slogan">注册一个账号，随时回来。</p>

          <form className="auth-form" onSubmit={handleSubmit} noValidate>
            {error && (
              <div className="form-message" role="alert">
                ⚠️ {error}
              </div>
            )}

            <label className="field">
              <span>昵称</span>
              <input
                type="text"
                value={username}
                onChange={e => { setUsername(e.target.value); setError(null) }}
                placeholder="朋友们怎么称呼你?"
                autoComplete="username"
                disabled={submitting}
              />
              <span className="field-hint">你的显示名，别人在频道里看到的名字。</span>
            </label>

            <label className="field">
              <span>邮箱</span>
              <input
                type="email"
                value={email}
                onChange={e => { setEmail(e.target.value); setError(null) }}
                placeholder="you@email.com"
                autoComplete="email"
                disabled={submitting}
              />
            </label>

            <label className="field">
              <span>密码</span>
              <input
                type="password"
                value={password}
                onChange={e => { setPassword(e.target.value); setError(null) }}
                placeholder="至少 8 位"
                autoComplete="new-password"
                disabled={submitting}
              />
            </label>

            <button className="primary-button" type="submit" disabled={submitting}>
              {submitting ? '创建中…' : '创建账号'}
            </button>
          </form>

          <p className="switch-line">
            已有账号?
            <Link to="/login">直接登录</Link>
          </p>
          <p className="auth-tip">🔒 注册即自动登录，稍后可再绑定更多登录方式。</p>
        </>
      )}
    </main>
  )
}
