import type { ReactNode } from 'react'

// SheetRowProps 底部面板单行成员的展示信息与操作按钮
interface SheetRowProps {
  name: string // 成员昵称
  subtitle: string // 副文案行，如「🙌 想发言」「副房主 · 已连麦」
  initial: string // 头像首字
  gradient: string // 头像渐变类（g1-g6）
  isSelf?: boolean // 是否当前用户，名字追加「· 我」
  actions?: ReactNode // 行尾管理按钮组，无权限时不渲染
}

// SheetRow 渲染底部面板的一行：头像 + 昵称/副文案 + 管理按钮，复用于上麦与成员两张面板
export default function SheetRow({ name, subtitle, initial, gradient, isSelf = false, actions }: SheetRowProps) {
  return (
    <li className="raise-row">
      <div className={`ra ${gradient}`}>{initial}</div>
      <div className="rname">
        <b>
          {name}
          {isSelf && ' · 我'}
        </b>
        <span>{subtitle}</span>
      </div>
      {actions && <div className="r-act">{actions}</div>}
    </li>
  )
}