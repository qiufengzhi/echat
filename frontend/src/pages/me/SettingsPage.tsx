import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import BackHeader from '../../components/layout/BackHeader'
import {
  getRoomJoinDefaults,
  persistRoomJoinDefaults,
  saveRoomJoinDefaults,
  syncRoomJoinDefaultsFromServer,
  type RoomJoinDefaults,
} from '../../services/settings'

// SETTING_ROWS 设置子页的静态列表项
const SETTING_ROWS = [
  { icon: '🌙', label: '深色模式', val: '›' },
  { icon: 'ℹ️', label: '关于', val: 'v0.1' },
] as const

// ToggleSetting 一行开关设置项：左侧图标与标签，右侧胶囊滑块，点击行整体切换
function ToggleSetting({
  icon,
  label,
  checked,
  onChange,
}: {
  icon: string
  label: string
  checked: boolean
  onChange: (next: boolean) => void
}) {
  return (
    <label className="row-item setting-toggle">
      <span className="ri">{icon}</span>
      <b>{label}</b>
      <span className="val">
        <input
          type="checkbox"
          role="switch"
          aria-checked={checked}
          checked={checked}
          onChange={e => onChange(e.target.checked)}
        />
        <span className="knob" aria-hidden="true" />
      </span>
    </label>
  )
}

// SettingsPage 我的-设置子页：进房默认行为开关（服务端持久化）+ 静态占位项
export default function SettingsPage() {
  const navigate = useNavigate()
  // 初始读本地镜像即时渲染，挂载后再拉服务端权威值覆盖
  const [roomDefaults, setRoomDefaults] = useState<RoomJoinDefaults>(getRoomJoinDefaults)
  const [syncError, setSyncError] = useState<string | null>(null)

  const handleBack = () => {
    // 从主界面点进来的用浏览器回退，直链打开时才退回主界面
    if (window.history.length > 1) navigate(-1)
    else navigate('/')
  }

  useEffect(() => {
    let alive = true
    void (async () => {
      const ok = await syncRoomJoinDefaultsFromServer()
      if (alive && ok) setRoomDefaults(getRoomJoinDefaults())
    })()
    return () => {
      alive = false
    }
  }, [])

  // 切换即乐观更新本地，再 PUT 服务端；失败回滚并提示，不静默
  // prev 以镜像为准而非闭包里的 roomDefaults，快速连点时不会丢上一次切换
  const updateDefault = async (patch: Partial<RoomJoinDefaults>) => {
    const prev = getRoomJoinDefaults()
    const next = { ...prev, ...patch }
    setRoomDefaults(next)
    saveRoomJoinDefaults(next)
    const ok = await persistRoomJoinDefaults(next)
    if (ok) {
      setSyncError(null)
    } else {
      saveRoomJoinDefaults(prev)
      setRoomDefaults(prev)
      setSyncError('保存失败，请稍后重试')
    }
  }

  return (
    <div className="me-sub">
      <BackHeader title="设置" onBack={handleBack} />

      <div className="setting-section-title">进入房间</div>
      <ToggleSetting
        icon="🎤"
        label="进入时开启麦克风"
        checked={roomDefaults.micOnByDefault}
        onChange={next => void updateDefault({ micOnByDefault: next })}
      />
      <ToggleSetting
        icon="🔊"
        label="进入时开启扬声器"
        checked={roomDefaults.speakerOnByDefault}
        onChange={next => void updateDefault({ speakerOnByDefault: next })}
      />
      {syncError && <p className="settings-sync-error">{syncError}</p>}

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