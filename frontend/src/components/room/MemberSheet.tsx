import type { User } from '../../hooks/useVoiceRoom'
import type { RoomRole } from '../../types/signaling'
import { avatarGradient } from './MemberSeat'

// ROLE_COPY 成员面板角色文案，与席位徽标共用同一套角色取值
const ROLE_COPY: Record<RoomRole, string> = {
  host: '房主', // 房主，创建房间的人，拥有全部管理权限
  cohost: '副房主', // 副房主，继承房主的管理权限
  speaker: '嘉宾', // 上麦嘉宾，可发言，受静音叠加覆盖
  listener: '听众', // 听众，默认角色，可举手申请上麦
}

// MemberSheetProps 底部成员面板需要的实时状态与操作
interface MemberSheetProps {
  isOpen: boolean // 面板是否展开
  users: User[] // 实时成员快照，含角色，来自 hook 状态
  waitingUserIds: Set<string> // 举手等待集合，叠加到对应成员行
  mutedUserIds: Set<string> // 被管理静音集合，叠加到对应成员行
  selfId: string | null // 当前登录用户 ID，用于「· 我」标记与「不管理自己」护栏
  canManage: boolean // 当前用户是否可管理（host/cohost），决定是否渲染管理按钮
  onClose: () => void // 关闭面板
  onApprove: (userId: string) => void // 批准目标举手成员上麦
  onReject: (userId: string) => void // 拒绝目标举手成员
  onKick: (userId: string) => void // 请目标 speaker 下麦
  onMute: (userId: string, muted: boolean) => void // 静音或解除目标 speaker
}

// SheetRowProps 单行成员的全部展示与操作信息
interface SheetRowProps {
  user: User // 成员数据
  isSelf: boolean // 是否为当前用户
  isWaiting: boolean // 是否正在举手等待
  isMutedByHost: boolean // 是否被管理静音
  canManage: boolean // 当前用户是否可管理
  onApprove: (userId: string) => void // 批准上麦回调
  onReject: (userId: string) => void // 拒绝举手回调
  onKick: (userId: string) => void // 请下麦回调
  onMute: (userId: string, muted: boolean) => void // 静音切换回调
}

// SheetRow 渲染单个成员行：头像 + 昵称/角色/状态展示 + 管理按钮
// 管理按钮仅在操作者有权且目标不是自己时渲染：举手成员给「上麦/暂缓」，speaker 给「下麦/静音」
function SheetRow({
  user,
  isSelf,
  isWaiting,
  isMutedByHost,
  canManage,
  onApprove,
  onReject,
  onKick,
  onMute,
}: SheetRowProps) {
  const gradient = avatarGradient(user.id)
  // 自己是管理对象或自己这一行时都只读，避免误操作自己
  const showActions = canManage && !isSelf && (isWaiting || user.role === 'speaker')

  return (
    <li className="raise-row">
      <div className={`ra ${gradient}`}>
        {user.username.slice(0, 1)}
        {isWaiting && <span className="raise-ic">🙌</span>}
      </div>

      <div className="rname">
        <span className="rn">
          {user.username}
          {isSelf && <b> · 我</b>}
        </span>
        <span className="rs">
          <span className="role-tag">{ROLE_COPY[user.role]}</span>
          {isWaiting && <span className="rw">举手中</span>}
          {isMutedByHost && <span className="rm">🔇</span>}
        </span>
      </div>

      {showActions && (
        <div className="r-act">
          {isWaiting && (
            <>
              <button className="mini-btn ok" type="button" onClick={() => onApprove(user.id)}>
                上麦
              </button>
              <button className="mini-btn no" type="button" onClick={() => onReject(user.id)}>
                暂缓
              </button>
            </>
          )}
          {user.role === 'speaker' && (
            <>
              <button className="mini-btn ok" type="button" onClick={() => onKick(user.id)}>
                下麦
              </button>
              <button className="mini-btn" type="button" onClick={() => onMute(user.id, !isMutedByHost)}>
                {isMutedByHost ? '解除静音' : '静音'}
              </button>
            </>
          )}
        </div>
      )}
    </li>
  )
}

// MemberSheet 频道页底部成员面板：成员 = 实时 users + waiting/muted 快速状态合成的视图
// 打开时覆盖在页面底部，点背景关闭；管理按钮按 canManage 显隐，self 行按 user_id 判「· 我」
export default function MemberSheet({
  isOpen,
  users,
  waitingUserIds,
  mutedUserIds,
  selfId,
  canManage,
  onClose,
  onApprove,
  onReject,
  onKick,
  onMute,
}: MemberSheetProps) {
  if (!isOpen) return null

  return (
    <>
      <div className="sheet-backdrop" onClick={onClose} aria-hidden="true" />
      <section className="sheet" role="dialog" aria-label="成员面板">
        <div className="sheet-inner">
          <div className="handle" />
          <header className="sheet-head">
            <h3>成员</h3>
            <button className="btn-close" type="button" aria-label="关闭成员面板" onClick={onClose}>
              ✕
            </button>
          </header>
          <p className="ms-sub">
            {users.length} 人在线 · {waitingUserIds.size} 人举手中
          </p>
          <ul className="raise-list">
            {users.map(user => (
              <SheetRow
                key={user.id}
                user={user}
                isSelf={user.id === selfId}
                isWaiting={waitingUserIds.has(user.id)}
                isMutedByHost={mutedUserIds.has(user.id)}
                canManage={canManage}
                onApprove={onApprove}
                onReject={onReject}
                onKick={onKick}
                onMute={onMute}
              />
            ))}
          </ul>
        </div>
      </section>
    </>
  )
}