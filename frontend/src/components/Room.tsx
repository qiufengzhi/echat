import { useEffect, useRef } from 'react'

interface RoomProps {
  roomId: string
  username: string
  voiceRoom: {
    isConnected: boolean
    isMuted: boolean
    isSpeakerOn: boolean
    localStream: MediaStream | null
    remoteStream: MediaStream | null
    error: string | null
    toggleMute: () => void
    toggleSpeaker: () => void
  }
  onLeave: () => void
}

export default function Room({ roomId, username, voiceRoom, onLeave }: RoomProps) {
  const { isConnected, isMuted, isSpeakerOn, localStream, remoteStream, error, toggleMute, toggleSpeaker } = voiceRoom
  
  const localAudioRef = useRef<HTMLAudioElement>(null)
  const remoteAudioRef = useRef<HTMLAudioElement>(null)

  // 播放本地音频 (可选，调试用)
  useEffect(() => {
    if (localAudioRef.current && localStream) {
      localAudioRef.current.srcObject = localStream
    }
  }, [localStream])

  // 播放远程音频
  useEffect(() => {
    if (remoteAudioRef.current && remoteStream) {
      remoteAudioRef.current.srcObject = remoteStream
    }
  }, [remoteStream])

  // 对方头像
  const avatar = localStream?.getAudioTracks().length ? '🎤' : '👤'
  const remoteAvatar = remoteStream ? '🎤' : '👤'

  return (
    <>
      <header className="room-header">
        <div className="room-info">
          <span>房间号</span>
          <span>{roomId}</span>
        </div>
        <button className="btn btn-danger" onClick={onLeave}>
          🚪 离开
        </button>
      </header>

      {/* 音频元素 */}
      <audio ref={localAudioRef} autoPlay muted />
      <audio ref={remoteAudioRef} autoPlay />

      {/* 连接状态 */}
      <div className="status">
        {error && (
          <div className="error">
            <p>❌ {error}</p>
          </div>
        )}
        {!error && !isConnected && (
          <div className="waiting">
            <p>⏳ 等待对方加入房间...</p>
            <p style={{ fontSize: '12px', color: '#666', marginTop: '8px' }}>
              让他们使用相同的房间号
            </p>
          </div>
        )}
        {isConnected && (
          <div className="connected">
            <p>✅ 已连接，开始通话!</p>
          </div>
        )}
      </div>

      {/* 麦位区域 */}
      <div className="mic-area">
        <div className="mic-slots">
          {/* 自己 */}
          <div className={`mic-slot ${localStream ? 'active' : ''}`}>
            <div className={`mic-avatar ${!localStream ? 'empty' : ''}`}>
              {avatar}
            </div>
            <span className="mic-name">
              {username} (我)
            </span>
            <span style={{ fontSize: '12px', color: isMuted ? '#ff4757' : '#2ed573' }}>
              {isMuted ? '🔇 静音' : '🎙️ 开麦'}
            </span>
          </div>

          {/* 对方 */}
          <div className={`mic-slot ${remoteStream ? 'speaking' : ''}`}>
            <div className={`mic-avatar ${!remoteStream ? 'empty' : ''}`}>
              {remoteAvatar}
            </div>
            <span className="mic-name">
              {remoteStream ? '对方' : '等待加入'}
            </span>
            <span style={{ fontSize: '12px', color: '#888' }}>
              {remoteStream ? '🟢 在线' : '⚪ 离线'}
            </span>
          </div>
        </div>

        {/* 控制按钮 */}
        <div className="mic-controls">
          <button 
            className={`mic-btn mute ${isMuted ? 'active' : ''}`}
            onClick={toggleMute}
            title={isMuted ? '取消静音' : '静音'}
          >
            {isMuted ? '🔇' : '🎤'}
          </button>
          <button 
            className={`mic-btn speaker ${isSpeakerOn ? 'active' : ''}`}
            onClick={toggleSpeaker}
            title={isSpeakerOn ? '关闭扬声器' : '打开扬声器'}
          >
            {isSpeakerOn ? '🔊' : '🔈'}
          </button>
        </div>
      </div>

      {/* 提示 */}
      <p className="hint">
        💡 点击麦克风按钮可以静音/取消静音
      </p>

      {/* 调试信息 */}
      <div className="user-list" style={{ marginTop: '20px', fontSize: '12px', color: '#666' }}>
        <h3>连接信息</h3>
        <p>本地音频: {localStream ? '✅ 已连接' : '❌ 未连接'}</p>
        <p>远程音频: {remoteStream ? '✅ 已连接' : '❌ 未连接'}</p>
        <p>WebRTC: {isConnected ? '✅ 连接成功' : '⏳ 等待连接'}</p>
      </div>
    </>
  )
}