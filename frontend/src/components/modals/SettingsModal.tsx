import { useState } from 'react'

import BaseModal from './BaseModal'

interface SettingsModalProps {
  isOpen: boolean // 设置弹窗是否显示。
  hasMicrophone: boolean // 当前是否已经拿到麦克风音频流。
  isMuted: boolean // 当前用户是否静音。
  isSpeakerOn: boolean // 当前是否播放房间声音。
  onToggleMute: () => void // 切换麦克风静音状态。
  onToggleSpeaker: () => void // 切换扬声器播放状态。
  onClose: () => void // 关闭设置弹窗。
}

// SettingsModal 先提供用户能理解的声音设置入口，设备枚举和真实切换后续可在这里继续接入。
export default function SettingsModal({
  isOpen,
  hasMicrophone,
  isMuted,
  isSpeakerOn,
  onToggleMute,
  onToggleSpeaker,
  onClose,
}: SettingsModalProps) {
  const [noiseSuppression, setNoiseSuppression] = useState(true)
  const [autoGain, setAutoGain] = useState(true)
  const [testResult, setTestResult] = useState<string | null>(null)

  const handleTestMicrophone = () => {
    setTestResult(hasMicrophone ? '麦克风准备好了' : '还没听到麦克风，检查权限后再试')
  }

  return (
    <BaseModal
      isOpen={isOpen}
      title="声音设置"
      description="把声音调到刚刚好"
      hideHeaderCopy
      closeLabel="关闭设置"
      onClose={onClose}
    >
      <div className="setting-group">
        <label className="field">
          <span>麦克风</span>
          <select defaultValue="default">
            <option value="default">默认麦克风</option>
          </select>
        </label>

        <label className="field">
          <span>扬声器</span>
          <select defaultValue="default">
            <option value="default">默认扬声器</option>
          </select>
        </label>
      </div>

      <div className="toggle-list" aria-label="声音开关">
        <label className="toggle-row">
          <span>
            <strong>麦克风</strong>
            <em>{isMuted ? '已静音，房间听不到你' : '可以开口了'}</em>
          </span>
          <input type="checkbox" checked={!isMuted} onChange={onToggleMute} />
        </label>

        <label className="toggle-row">
          <span>
            <strong>扬声器</strong>
            <em>{isSpeakerOn ? '房间声音开着' : '房间声音已关'}</em>
          </span>
          <input type="checkbox" checked={isSpeakerOn} onChange={onToggleSpeaker} />
        </label>

        <label className="toggle-row">
          <span>
            <strong>降噪</strong>
            <em>少一点背景声</em>
          </span>
          <input
            type="checkbox"
            checked={noiseSuppression}
            onChange={event => setNoiseSuppression(event.target.checked)}
          />
        </label>

        <label className="toggle-row">
          <span>
            <strong>自动增益</strong>
            <em>自动稳住音量</em>
          </span>
          <input type="checkbox" checked={autoGain} onChange={event => setAutoGain(event.target.checked)} />
        </label>
      </div>

      <button className="primary-button full-width" type="button" onClick={handleTestMicrophone}>
        试一下麦克风
      </button>

      {testResult && <p className="modal-footnote">{testResult}</p>}
    </BaseModal>
  )
}
