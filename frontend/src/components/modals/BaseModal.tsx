import type { ReactNode } from 'react'

interface BaseModalProps {
  isOpen: boolean // 弹窗是否显示。
  title: string // 弹窗标题。
  description: string // 标题下方的说明文案。
  hideHeaderCopy?: boolean // 是否隐藏可见标题和说明，仅保留无障碍标题给读屏器。
  closeLabel: string // 关闭按钮的无障碍说明。
  children: ReactNode // 弹窗主体内容。
  onClose: () => void // 用户点击关闭或遮罩时触发。
}

// BaseModal 提供统一的遮罩、标题和关闭按钮，让设置/邀请弹窗保持相同交互。
export default function BaseModal({
  isOpen,
  title,
  description,
  hideHeaderCopy = false,
  closeLabel,
  children,
  onClose,
}: BaseModalProps) {
  if (!isOpen) return null

  return (
    <div className="modal-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="modal-card"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={event => event.stopPropagation()}
      >
        <header className={`modal-header ${hideHeaderCopy ? 'is-copy-hidden' : ''}`}>
          {!hideHeaderCopy && (
            <div>
              <h2>{title}</h2>
              <p>{description}</p>
            </div>
          )}
          <button className="close-button" type="button" aria-label={closeLabel} onClick={onClose}>
            ×
          </button>
        </header>
        {children}
      </section>
    </div>
  )
}
