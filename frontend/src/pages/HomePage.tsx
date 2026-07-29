import { useEffect, useState } from 'react'
import type { JoinRoomInput } from '../types/voiceRoomUi'

interface HomePageProps {
  defaultRoomId: string
  defaultUsername: string
  error: string | null
  isJoining: boolean
  onCreateRoom: (input: JoinRoomInput) => void
  onJoinRoom: (input: JoinRoomInput) => void
}

// HomePage 是苍月草的首页：Logo + 昵称/房间号 + 创建/加入
export default function HomePage({
  defaultRoomId,
  defaultUsername,
  error,
  isJoining,
  onCreateRoom,
  onJoinRoom,
}: HomePageProps) {
  const [roomId, setRoomId] = useState(defaultRoomId)
  const [username, setUsername] = useState(defaultUsername)
  const [formError, setFormError] = useState<string | null>(null)

  useEffect(() => {
    setRoomId(defaultRoomId)
    setUsername(defaultUsername)
  }, [defaultRoomId, defaultUsername])

  const trimmedUsername = username.trim()
  const trimmedRoomId = roomId.trim().toUpperCase()
  const visibleError = formError || error

  const validate = () => {
    if (!trimmedUsername) {
      setFormError('取个昵称再进来吧')
      return false
    }
    setFormError(null)
    return true
  }

  const handleCreate = () => {
    if (!validate()) return
    onCreateRoom({ roomId: trimmedRoomId, username: trimmedUsername })
  }

  const handleJoin = () => {
    if (!validate()) return
    if (!trimmedRoomId) {
      setFormError('填上朋友给你的房间号')
      return
    }
    onJoinRoom({ roomId: trimmedRoomId, username: trimmedUsername })
  }

  return (
    <main className="home-page">
      <div className="home-top">
        <div className="wordmark"><span className="d" />苍月草</div>
      </div>

      <div className="home-body">
        <div className="home-hero">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" width="100" height="100" style={{ display: 'block', margin: '0 auto' }}>
            <defs>
              <linearGradient id="logo-bg" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stopColor="#FFF9F1" />
                <stop offset="1" stopColor="#FFEBD3" />
              </linearGradient>
            </defs>
            <rect x="0" y="0" width="512" height="512" rx="116" fill="url(#logo-bg)" />
            <ellipse cx="256" cy="376" rx="142" ry="16" fill="#EFD8BC" opacity="0.55" />
            <path d="M132,156 q3,10 13,13 q-10,3 -13,13 q-3,-10 -13,-13 q10,-3 13,-13 z" fill="#F5C88F" opacity="0.9" />
            <path d="M392,320 q2.4,8 10.4,10.4 q-8,2.4 -10.4,10.4 q-2.4,-8 -10.4,-10.4 q8,-2.4 10.4,-10.4 z" fill="#F5C88F" opacity="0.7" />
            <line x1="88" y1="314" x2="424" y2="234" stroke="#C89B6D" strokeWidth="9" strokeLinecap="round" />
            <circle cx="168" cy="294" r="54" fill="#F6A9BC" />
            <ellipse cx="150" cy="274" rx="17" ry="11" fill="#FFFFFF" opacity="0.35" transform="rotate(-25 150 274)" />
            <path d="M151,288 Q157,280 163,288" stroke="#57453A" strokeWidth="5" fill="none" strokeLinecap="round" />
            <path d="M173,288 Q179,280 185,288" stroke="#57453A" strokeWidth="5" fill="none" strokeLinecap="round" />
            <circle cx="168" cy="301" r="5" fill="#57453A" />
            <ellipse cx="145" cy="299" rx="7" ry="4.5" fill="#F27E9C" opacity="0.55" />
            <ellipse cx="191" cy="299" rx="7" ry="4.5" fill="#F27E9C" opacity="0.55" />
            <circle cx="344" cy="254" r="54" fill="#A7CE9C" />
            <ellipse cx="326" cy="234" rx="17" ry="11" fill="#FFFFFF" opacity="0.35" transform="rotate(-25 326 234)" />
            <path d="M327,248 Q333,240 339,248" stroke="#57453A" strokeWidth="5" fill="none" strokeLinecap="round" />
            <path d="M349,248 Q355,240 361,248" stroke="#57453A" strokeWidth="5" fill="none" strokeLinecap="round" />
            <circle cx="344" cy="261" r="5" fill="#57453A" />
            <ellipse cx="321" cy="259" rx="7" ry="4.5" fill="#E89AAB" opacity="0.5" />
            <ellipse cx="367" cy="259" rx="7" ry="4.5" fill="#E89AAB" opacity="0.5" />
            <circle cx="256" cy="274" r="54" fill="#FFFDF9" stroke="#F0E1CC" strokeWidth="4" />
            <path d="M206,272 A50,50 0 0 1 306,272" stroke="#6B5A4E" strokeWidth="9" fill="none" strokeLinecap="round" />
            <rect x="197" y="260" width="17" height="26" rx="8" fill="#6B5A4E" />
            <rect x="298" y="260" width="17" height="26" rx="8" fill="#6B5A4E" />
            <path d="M306,286 Q302,301 285,302" stroke="#6B5A4E" strokeWidth="6" fill="none" strokeLinecap="round" />
            <circle cx="281" cy="302" r="6" fill="#6B5A4E" />
            <path d="M239,268 Q245,260 251,268" stroke="#57453A" strokeWidth="5" fill="none" strokeLinecap="round" />
            <path d="M261,268 Q267,260 273,268" stroke="#57453A" strokeWidth="5" fill="none" strokeLinecap="round" />
            <ellipse cx="256" cy="283" rx="7" ry="8" fill="#57453A" />
            <ellipse cx="256" cy="286.5" rx="4" ry="3.5" fill="#F28BA6" />
            <ellipse cx="233" cy="279" rx="8" ry="5" fill="#F2A2B4" opacity="0.6" />
            <ellipse cx="279" cy="279" rx="8" ry="5" fill="#F2A2B4" opacity="0.6" />
          </svg>
        </div>

        {visibleError && (
          <div className="form-message" role="alert">
            ⚠️ {visibleError}
          </div>
        )}

        <label className="field">
          <span>昵称</span>
          <input
            type="text"
            value={username}
            onChange={e => { setUsername(e.target.value); setFormError(null) }}
            maxLength={20}
            disabled={isJoining}
            placeholder="你的昵称"
          />
        </label>

        <label className="field">
          <span>房间号</span>
          <input
            type="text"
            value={roomId}
            onChange={e => { setRoomId(e.target.value.toUpperCase()); setFormError(null) }}
            maxLength={12}
            disabled={isJoining}
            placeholder="留空则创建新房间"
          />
        </label>

        <div className="home-actions">
          <button className="primary-button" type="button" onClick={handleCreate} disabled={isJoining}>
            {isJoining ? '正在准备…' : '创建房间'}
          </button>
          <button className="secondary-button" type="button" onClick={handleJoin} disabled={isJoining}>
            加入房间
          </button>
        </div>
      </div>
    </main>
  )
}
