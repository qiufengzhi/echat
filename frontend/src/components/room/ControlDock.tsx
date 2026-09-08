import type { RoomRole } from '../../types/signaling'

// ActiveSheet 当前展开的底部面板，用于高亮对应控制键
type ActiveSheet = 'raise' | 'members' | null

// ControlDockProps 描述频道页底部控制栏需要的状态与操作
interface ControlDockProps {
  isMuted: boolean // 本地麦克风是否静音，驱动主按钮置灰
  isSpeakerOn: boolean // 是否播放远端声音
  isAIEnabled: boolean // AI 是否处于非离线状态
  canManage: boolean // 当前用户是否可管理（host/cohost），控制 AI 开关生效
  selfRole: RoomRole | null // 当前用户角色，听众的上麦键退化为举手动作
  isWaiting: boolean // 自己是否已举手，举手态禁用上麦键
  activeSheet: ActiveSheet // 当前展开的面板，高亮对应的键
  onToggleMute: () => void // 切换本地麦克风静音
  onToggleSpeaker: () => void // 切换扬声器播放开关
  onToggleAI: () => void // 切换 AI 语音助手开关
  onRaiseHand: () => void // 听众举手请求上麦
  onToggleSheet: (sheet: 'raise' | 'members') => void // 打开/关闭指定底部面板
}

// ControlDock 频道页底部控制栏，按设计稿顺序排布五键并带文字标签：
// 麦克风(main) → 扬声器 → AI 助手 → 上麦 → 成员
// 上麦键对听众是举手动作（已举手则禁用），对管理者是打开上麦面板
export default function ControlDock({
  isMuted,
  isSpeakerOn,
  isAIEnabled,
  canManage,
  selfRole,
  isWaiting,
  activeSheet,
  onToggleMute,
  onToggleSpeaker,
  onToggleAI,
  onRaiseHand,
  onToggleSheet,
}: ControlDockProps) {
  const isListener = selfRole === 'listener'

  return (
    <nav className="control-dock" aria-label="频道控制栏">
      <button
        className={`db main ${isMuted ? 'off' : ''}`}
        type="button"
        aria-label={isMuted ? '取消静音' : '静音'}
        onClick={onToggleMute}
      >
        <span className="ic">🎤</span>
        <span className="lb">麦克风</span>
      </button>

      <button
        className={`db ${isSpeakerOn ? '' : 'off'}`}
        type="button"
        aria-label={isSpeakerOn ? '关闭声音' : '打开声音'}
        onClick={onToggleSpeaker}
      >
        <span className="ic">{isSpeakerOn ? '🔊' : '🔈'}</span>
        <span className="lb">扬声器</span>
      </button>

      <button
        className={`db ${isAIEnabled ? '' : 'off'}`}
        type="button"
        aria-label="AI 语音助手"
        aria-disabled={!canManage}
        onClick={canManage ? onToggleAI : undefined}
      >
        <span className="ic">✨</span>
        <span className="lb">AI 助手</span>
      </button>

      <button
        className={`db ${activeSheet === 'raise' ? 'on' : ''}`}
        type="button"
        aria-label={isListener ? (isWaiting ? '已举手，等待上麦' : '举手请求上麦') : '上麦面板'}
        aria-disabled={isListener && isWaiting}
        onClick={isListener ? onRaiseHand : () => onToggleSheet('raise')}
        disabled={isListener && isWaiting}
      >
        <span className="ic">👥</span>
        <span className="lb">{isListener && isWaiting ? '已举手' : '上麦'}</span>
      </button>

      <button
        className={`db ${activeSheet === 'members' ? 'on' : ''}`}
        type="button"
        aria-label="成员面板"
        onClick={() => onToggleSheet('members')}
      >
        <span className="ic">⚙</span>
        <span className="lb">成员</span>
      </button>
    </nav>
  )
}