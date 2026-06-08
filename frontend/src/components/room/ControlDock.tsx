interface ControlDockProps {
  isMuted: boolean // 当前用户是否静音。
  isSpeakerOn: boolean // 当前是否播放房间声音。
  onToggleMute: () => void // 点击麦克风按钮时触发。
  onToggleSpeaker: () => void // 点击扬声器按钮时触发。
  onOpenMembers: () => void // 点击成员按钮时触发。
  onOpenInvite: () => void // 点击邀请按钮时触发。
  onOpenSettings: () => void // 点击设置按钮时触发。
  onLeave: () => void // 点击离开按钮时触发。
}

// ControlDock 是声聊间的高频控制栏，所有按钮都用自然语言 title 说明点击结果。
export default function ControlDock({
  isMuted,
  isSpeakerOn,
  onToggleMute,
  onToggleSpeaker,
  onOpenMembers,
  onOpenInvite,
  onOpenSettings,
  onLeave,
}: ControlDockProps) {
  return (
    <nav className="control-dock" aria-label="声聊间控制栏">
      <button
        className={`icon-button ${!isMuted ? 'primary' : 'danger-soft'}`}
        type="button"
        aria-label={isMuted ? '取消静音' : '静音'}
        title={isMuted ? '取消静音' : '静音'}
        onClick={onToggleMute}
      >
        {isMuted ? '🔇' : '🎙'}
      </button>
      <button
        className={`icon-button ${isSpeakerOn ? '' : 'danger-soft'}`}
        type="button"
        aria-label={isSpeakerOn ? '关闭房间声音' : '打开房间声音'}
        title={isSpeakerOn ? '关闭房间声音' : '打开房间声音'}
        onClick={onToggleSpeaker}
      >
        {isSpeakerOn ? '🔊' : '🔈'}
      </button>
      <button className="icon-button" type="button" aria-label="查看成员" title="查看成员" onClick={onOpenMembers}>
        👥
      </button>
      <button className="icon-button" type="button" aria-label="邀请好友" title="邀请好友" onClick={onOpenInvite}>
        ↗
      </button>
      <button className="icon-button" type="button" aria-label="打开设置" title="打开设置" onClick={onOpenSettings}>
        ⚙
      </button>
      <button className="icon-button danger" type="button" aria-label="离开房间" title="离开房间" onClick={onLeave}>
        ⏻
      </button>
    </nav>
  )
}
