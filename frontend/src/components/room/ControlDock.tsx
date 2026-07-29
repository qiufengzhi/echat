interface ControlDockProps {
  isMuted: boolean
  isSpeakerOn: boolean
  isHost: boolean
  isAIEnabled: boolean
  onToggleMute: () => void
  onToggleSpeaker: () => void
  onToggleAI: () => void
  onLeave: () => void
}

// ControlDock 底部控制栏：声音 + 麦克风（居中主按钮）+ 离开，房主额外 AI 按钮
export default function ControlDock({
  isMuted,
  isSpeakerOn,
  isHost,
  isAIEnabled,
  onToggleMute,
  onToggleSpeaker,
  onToggleAI,
  onLeave,
}: ControlDockProps) {
  return (
    <nav className="control-dock" aria-label="声聊间控制栏">
      <button
        className={`db ${!isSpeakerOn ? 'off' : ''}`}
        type="button"
        aria-label={isSpeakerOn ? '关闭声音' : '打开声音'}
        onClick={onToggleSpeaker}
      >
        <span className="ic">{isSpeakerOn ? '🔊' : '🔈'}</span>
      </button>

      {isHost && (
        <button
          className={`db ${!isAIEnabled ? 'off' : ''}`}
          type="button"
          aria-label={isAIEnabled ? '关闭 AI' : '开启 AI'}
          onClick={onToggleAI}
        >
          <span className="ic">🤖</span>
        </button>
      )}

      <button
        className={`db main ${isMuted ? 'off' : ''}`}
        type="button"
        aria-label={isMuted ? '取消静音' : '静音'}
        onClick={onToggleMute}
      >
        <span className="ic">{isMuted ? '🔇' : '🎙️'}</span>
      </button>

      <button className="db off" type="button" aria-label="离开房间" onClick={onLeave}>
        <span className="ic">✕</span>
      </button>
    </nav>
  )
}
