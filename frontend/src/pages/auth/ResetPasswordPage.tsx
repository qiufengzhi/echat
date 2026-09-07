import { useState } from 'react'
import { Link } from 'react-router-dom'

// ResetPasswordPage 重置密码入口页：只收邮箱，后端防枚举恒返回成功，统一提示已发送
export default function ResetPasswordPage() {
  const [email, setEmail] = useState('') // 用户输入的邮箱
  const [submitting, setSubmitting] = useState(false) // 请求中置灰按钮
  const [sent, setSent] = useState(false) // 是否已提交并展示"已发送"提示

  // handleSubmit 调 password/reset-request；无论邮箱是否存在都提示已发送（防枚举）
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email.trim()) {
      setSent(true) // 空输入也走同一提示，不区分字段校验细节
      return
    }
    setSubmitting(true)
    try {
      await fetch('/api/v1/auth/password/reset-request', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: email.trim() }),
      })
      setSent(true)
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

      {sent ? (
        <>
          <div className="h1">重置链接已发送</div>
          <p className="slogan">如果该邮箱已注册，你会收到一封重置密码的邮件。</p>
          <Link className="primary-button auth-mt" to="/login">
            返回登录
          </Link>
        </>
      ) : (
        <>
          <div className="h1">找回密码</div>
          <p className="slogan">输入注册邮箱，我们会发一封重置链接给你。</p>

          <form className="auth-form" onSubmit={handleSubmit} noValidate>
            <label className="field">
              <span>邮箱</span>
              <input
                type="email"
                value={email}
                onChange={e => setEmail(e.target.value)}
                placeholder="you@email.com"
                autoComplete="email"
                disabled={submitting}
              />
            </label>
            <button className="primary-button" type="submit" disabled={submitting}>
              {submitting ? '发送中…' : '发送重置链接'}
            </button>
          </form>

          <p className="switch-line">
            想起密码了?
            <Link to="/login">返回登录</Link>
          </p>
        </>
      )}
    </main>
  )
}