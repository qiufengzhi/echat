import type { RoomRole } from '../../types/signaling'

// ControlDockProps 描述频道页底部控制栏需要的状态与操作
interface ControlDockProps {
  isMuted: boolean // 本地麦克风是否静音，驱动主按钮亮灭
  isSpeakerOn: boolean // 是否播放远端声音
  isAIEnabled: boolean // AI 是否处于非离线状态，驱动 AI 按钮亮灭
  canManage: boolean // 当前用户是否可管理（host/cohost），控制 AI 按钮显隐
  selfRole: RoomRole | null // 当前用户角色，听众才显示上麦按钮
  isWaiting: boolean // 自己是否已举手，已举手时上麦按钮禁用并显示「已举手」
  isMemberOpen: boolean // 成员面板是否打开，用于高亮成员按钮
  onToggleMute: () => void // 切换本地麦克风静音
  onToggleSpeaker: () => void // 切换扬声器播放开关
  onToggleAI: () => void // 切换 AI 语音助手开关
  onRaiseHand: () => void // 听众举手请求上麦
  onToggleMembers: () => void // 打开/关闭成员面板
}

// ControlDock 频道页底部控制栏：扬声器 / AI / 上麦 / 成员 / 麦克风主按钮
// AI 仅管理可见，上麦仅听众可见，成员人人可见，麦克风始终是主按钮
export default function ControlDock({
  isMuted,
  isSpeakerOn,
  isAIEnabled,
  canManage,
  selfRole,
  isWaiting,
  isMemberOpen,
  onToggleMute,
  onToggleSpeaker,
  onToggleAI,
  onRaiseHand,
  onToggleMembers,
}: ControlDockProps) {
  return (
    <nav className="control-dock" aria-label="频道控制栏">
      <button
        className={`db ${!isSpeakerOn ? 'off' : ''}`}
        type="button"
        aria-label={isSpeakerOn ? '关闭声音' : '打开声音'}
        onClick={onToggleSpeaker}
      >
        <span className="ic">{isSpeakerOn ? '🔊' : '🔈'}</span>
      </button>

      {canManage && (
        <button
          className={`db ${!isAIEnabled ? 'off' : ''}`}
          type="button"
          aria-label={isAIEnabled ? '关闭 AI' : '开启 AI'}
          onClick={onToggleAI}
        >
          <span className="ic">🤖</span>
        </button>
      )}

      {selfRole === 'listener' && (
        <button
          className="db"
          type="button"
          aria-label={isWaiting ? '已举手，等待上麦' : '举手请求上麦'}
          onClick={onRaiseHand}
          disabled={isWaiting}
        >
          <span className="ic">{isWaiting ? '🙌' : '🙋'}</span>
        </button>
      )}

      <button
        className={`db ${isMemberOpen ? 'on' : ''}`}
        type="button"
        aria-label="成员面板"
        onClick={onToggleMembers}
      >
        <span className="ic">👥</span>
      </button>

      <button
        className={`db main ${isMuted ? 'off' : ''}`}
        type="button"
        aria-label={isMuted ? '取消静音' : '静音'}
        onClick={onToggleMute}
      >
        <span className="ic">{isMuted ? '🔇' : '🎙️'}</span>
      </button>
    </nav>
  )
}