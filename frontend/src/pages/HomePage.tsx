import { useEffect, useState } from 'react'

import type { JoinRoomInput } from '../types/voiceRoomUi'

interface HomePageProps {
  defaultRoomId: string // 页面首次打开或重新加入时预填的房间号
  defaultUsername: string // 页面首次打开或重新加入时预填的昵称
  error: string | null // 加入流程中需要展示给用户的错误提示
  isJoining: boolean // 是否正在请求麦克风并进入房间，用于禁用按钮
  onCreateRoom: (input: JoinRoomInput) => void // 用户点击创建房间时触发
  onJoinRoom: (input: JoinRoomInput) => void // 用户点击加入房间时触发
  onOpenSettings: () => void // 用户点击设备检测时打开设置弹窗
}

// HomePage 是声聊间的入口页，负责收集昵称和房间号，并把创建/加入动作交给 App 处理
export default function HomePage({
  defaultRoomId,
  defaultUsername,
  error,
  isJoining,
  onCreateRoom,
  onJoinRoom,
  onOpenSettings,
}: HomePageProps) {
  const [roomId, setRoomId] = useState(defaultRoomId)
  const [username, setUsername] = useState(defaultUsername)
  const [formError, setFormError] = useState<string | null>(null)

  // 当用户从离开页回到入口时，App 会传入上一次房间信息，这里同步到表单里
  useEffect(() => {
    setRoomId(defaultRoomId)
    setUsername(defaultUsername)
  }, [defaultRoomId, defaultUsername])

  const trimmedUsername = username.trim()
  const trimmedRoomId = roomId.trim().toUpperCase()
  const visibleError = formError || error

  const validateUsername = () => {
    if (!trimmedUsername) {
      setFormError('先取个昵称，朋友进来才知道是你')
      return false
    }

    setFormError(null)
    return true
  }

  const handleCreateRoom = () => {
    if (!validateUsername()) return

    onCreateRoom({
      roomId: trimmedRoomId,
      username: trimmedUsername,
    })
  }

  const handleJoinRoom = () => {
    if (!validateUsername()) return

    if (!trimmedRoomId) {
      setFormError('填上朋友给你的房间号，就能进同一间声聊间')
      return
    }

    onJoinRoom({
      roomId: trimmedRoomId,
      username: trimmedUsername,
    })
  }

  return (
    <main className="home-page">
      <section className="home-hero" aria-labelledby="home-title">
        <nav className="top-nav" aria-label="首页导航">
          <div className="brand-mark" aria-label="eChat">
            eChat
          </div>
          <button className="text-button" type="button" onClick={onOpenSettings}>
            检查设备
          </button>
        </nav>

        <div className="home-copy">
          <p className="eyebrow">隔着屏幕，也能听见彼此</p>
          <h1 id="home-title">给朋友留一间有声音的小房间</h1>
          <p className="home-intro">
            输入昵称和房间号，打开麦克风就能和朋友聊天
          </p>
        </div>

        <div className="feature-strip" aria-label="声聊间特点">
          <div className="feature-pill">
            <strong>多人席位</strong>
            <span>谁在房间，一眼看见</span>
          </div>
          <div className="feature-pill">
            <strong>打开就聊</strong>
            <span>不用安装，打开网页就能聊</span>
          </div>
          <div className="feature-pill">
            <strong>一键邀请</strong>
            <span>复制链接，把房间发给朋友</span>
          </div>
        </div>
      </section>

      <section className="entry-panel" aria-label="进入声聊间表单">
        <div className="panel-heading">
          <div>
            <h2>进入声聊间</h2>
          </div>
          <span className="soft-badge">待开麦</span>
        </div>

        {visibleError && (
          <div className="form-message" role="alert">
            {visibleError}
          </div>
        )}

        <label className="field">
          <span>昵称</span>
          <input
            type="text"
            value={username}
            onChange={event => {
              setUsername(event.target.value)
              setFormError(null)
            }}
            maxLength={20}
            disabled={isJoining}
          />
        </label>

        <label className="field">
          <span>房间号</span>
          <input
            type="text"
            value={roomId}
            onChange={event => {
              setRoomId(event.target.value.toUpperCase())
              setFormError(null)
            }}
            maxLength={12}
            disabled={isJoining}
          />
        </label>

        <div className="entry-actions">
          <button className="primary-button" type="button" onClick={handleCreateRoom} disabled={isJoining}>
            {isJoining ? '正在准备' : '创建房间'}
          </button>
          <button className="secondary-button" type="button" onClick={handleJoinRoom} disabled={isJoining}>
            加入房间
          </button>
        </div>

        <div className="permission-note">
          <strong>先开麦克风</strong>
          <span>进入时会请求权限，听不到声音可以去声音设置里重试</span>
        </div>
      </section>
    </main>
  )
}
