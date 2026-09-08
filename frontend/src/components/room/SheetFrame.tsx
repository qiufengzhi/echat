import type { ReactNode } from 'react'

// SheetFrameProps 底部面板的公共结构
interface SheetFrameProps {
  title: string // 面板标题，同时作为无障碍标签
  sub?: string // 面板说明副文案，缺省不渲染
  onClose: () => void // 关闭面板
  children: ReactNode // 面板内容（成员行列表）
}

// SheetFrame 渲染底部面板的公共壳：遮罩 + 圆角卡 + 拖拽把手 + 标题 + 关闭按钮 + 内容
export default function SheetFrame({ title, sub, onClose, children }: SheetFrameProps) {
  return (
    <>
      <div className="sheet-backdrop" onClick={onClose} aria-hidden="true" />
      <section className="sheet" role="dialog" aria-label={title}>
        <div className="sheet-inner">
          <div className="handle" />
          <header className="sheet-head">
            <h3>{title}</h3>
            <button className="btn-close" type="button" aria-label="关闭面板" onClick={onClose}>
              ✕
            </button>
          </header>
          {sub && <p className="ms-sub">{sub}</p>}
          <ul className="raise-list">{children}</ul>
        </div>
      </section>
    </>
  )
}