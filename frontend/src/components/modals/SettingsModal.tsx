import { useEffect, useState } from 'react'

import BaseModal from './BaseModal'
import type { AudioDevice } from '../../hooks/useVoiceRoom'

interface SettingsModalProps {
  isOpen: boolean // 设置弹窗是否显示。
  hasMicrophone: boolean // 当前是否已经拿到麦克风音频流。
  isMuted: boolean // 当前用户是否静音。
  isSpeakerOn: boolean // 当前是否播放房间声音。
  availableMicrophones: AudioDevice[] // 可用的麦克风设备列表。
  availableSpeakers: AudioDevice[] // 可用的扬声器设备列表。
  currentMicrophoneId: string | null // 当前选中的麦克风设备 ID。
  onToggleMute: () => void // 切换麦克风静音状态。
  onToggleSpeaker: () => void // 切换扬声器播放状态。
  onRefreshDevices: () => void // 刷新音频设备列表。
  onSwitchMicrophone: (deviceId: string) => void // 切换麦克风设备。
  onClose: () => void // 关闭设置弹窗。
}

// SettingsModal 麦克风、扬声器设置弹窗。
export default function SettingsModal({
  isOpen,
  hasMicrophone,
  isMuted,
  isSpeakerOn,
  availableMicrophones,
  availableSpeakers,
  currentMicrophoneId,
  onToggleMute,
  onToggleSpeaker,
  onRefreshDevices,
  onSwitchMicrophone,
  onClose,
}: SettingsModalProps) {
  const [noiseSuppression, setNoiseSuppression] = useState(true)
  const [autoGain, setAutoGain] = useState(true)
  const [testResult, setTestResult] = useState<string | null>(null)

  // 弹窗打开时刷新设备列表，确保下拉框显示最新可用设备。
  useEffect(() => {
    if (isOpen) {
      onRefreshDevices()
    }
  }, [isOpen, onRefreshDevices])

  const handleTestMicrophone = () => {
    setTestResult(hasMicrophone ? '麦克风准备好了' : '还没听到麦克风，检查权限后再试')
  }

  const handleMicrophoneChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    const deviceId = event.target.value
    // 'default' 表示使用系统默认麦克风，传入空字符串让 hook 忽略 deviceId。
    onSwitchMicrophone(deviceId === 'default' ? '' : deviceId)
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
          <select
            value={currentMicrophoneId || 'default'}
            onChange={handleMicrophoneChange}
          >
            <option value="default">默认麦克风</option>
            {availableMicrophones.map(mic => (
              <option key={mic.deviceId} value={mic.deviceId}>
                {mic.label}
              </option>
            ))}
          </select>
        </label>

        <label className="field">
          <span>扬声器</span>
          <select defaultValue="default">
            <option value="default">默认扬声器</option>
            {availableSpeakers.map(speaker => (
              <option key={speaker.deviceId} value={speaker.deviceId}>
                {speaker.label}
              </option>
            ))}
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
