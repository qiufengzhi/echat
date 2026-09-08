import type { User } from '../../hooks/useVoiceRoom'
import { avatarGradient } from './MemberSeat'
import SheetFrame from './SheetFrame'
import SheetRow from './SheetRow'

// RaiseSheetProps 上麦面板需要的举手队列状态与审批操作
interface RaiseSheetProps {
  isOpen: boolean // 面板是否展开
  users: User[] // 实时成员快照，含角色
  waitingUserIds: Set<string> // 举手等待集合，决定哪些成员进入队列
  selfId: string | null // 当前登录用户 ID，用于「· 我」标记与「不管理自己」护栏
  canManage: boolean // 当前用户是否可管理（host/cohost），决定是否渲染审批按钮
  onClose: () => void // 关闭面板
  onApprove: (userId: string) => void // 批准目标举手成员上麦
  onReject: (userId: string) => void // 拒绝目标举手成员
}

// RaiseSheet 上麦底部面板「有人想上麦」：只列举手等待的成员，房主/副房主可上麦或暂缓
export default function RaiseSheet({
  isOpen,
  users,
  waitingUserIds,
  selfId,
  canManage,
  onClose,
  onApprove,
  onReject,
}: RaiseSheetProps) {
  if (!isOpen) return null

  const waitingMembers = users.filter(user => waitingUserIds.has(user.id))

  return (
    <SheetFrame title="有人想上麦" onClose={onClose}>
      {waitingMembers.length === 0 && <li className="sheet-empty">暂时没人举手</li>}
      {waitingMembers.map(user => {
        const isSelf = user.id === selfId
        const showActions = canManage && !isSelf
        return (
          <SheetRow
            key={user.id}
            name={user.username}
            subtitle="🙌 想发言"
            initial={user.username.slice(0, 1)}
            gradient={avatarGradient(user.id)}
            isSelf={isSelf}
            actions={
              showActions ? (
                <>
                  <button className="mini-btn ok" type="button" onClick={() => onApprove(user.id)}>
                    上麦
                  </button>
                  <button className="mini-btn no" type="button" onClick={() => onReject(user.id)}>
                    暂缓
                  </button>
                </>
              ) : undefined
            }
          />
        )
      })}
    </SheetFrame>
  )
}