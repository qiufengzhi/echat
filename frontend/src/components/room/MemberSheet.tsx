import type { User } from '../../hooks/useVoiceRoom'
import type { RoomRole } from '../../types/signaling'
import { avatarGradient } from './MemberSeat'
import SheetFrame from './SheetFrame'
import SheetRow from './SheetRow'

// ROLE_COPY 成员面板角色文案，与席位徽标共用同一套角色取值
const ROLE_COPY: Record<RoomRole, string> = {
  host: '房主', // 房主，创建房间的人，拥有全部管理权限
  cohost: '副房主', // 副房主，继承房主的管理权限
  speaker: '嘉宾', // 上麦嘉宾，可发言，受静音叠加覆盖
  listener: '听众', // 听众，默认角色，可举手申请上麦
}

// memberSub 组合成员行的副文案：举手、静音、已连麦、角色本身依次兜底
function memberSub(user: User, isWaiting: boolean, isMutedByHost: boolean): string {
  if (isWaiting) return '🙌 想发言'
  const role = ROLE_COPY[user.role]
  if (isMutedByHost) return `${role} · 已静音`
  if (user.role === 'speaker' || user.role === 'cohost') return `${role} · 已连麦`
  return role
}

// MemberSheetProps 成员底部面板需要的实时状态与操作
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

// MemberSheet 成员底部面板：成员 = 实时 users + waiting/muted 快速状态叠加的视图
// 举手成员给「上麦/暂缓」，speaker 给「下麦/静音」，self 行与无权成员只读
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
    <SheetFrame title="成员" onClose={onClose}>
      {users.map(user => {
        const isSelf = user.id === selfId
        const isWaiting = waitingUserIds.has(user.id)
        const isMutedByHost = mutedUserIds.has(user.id)
        const showActions = canManage && !isSelf && (isWaiting || user.role === 'speaker')
        return (
          <SheetRow
            key={user.id}
            name={user.username}
            subtitle={memberSub(user, isWaiting, isMutedByHost)}
            initial={user.username.slice(0, 1)}
            gradient={avatarGradient(user.id)}
            isSelf={isSelf}
            actions={
              showActions ? (
                <>
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
                </>
              ) : undefined
            }
          />
        )
      })}
    </SheetFrame>
  )
}