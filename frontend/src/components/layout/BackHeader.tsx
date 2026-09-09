import type { ReactNode } from 'react'

// BackHeaderProps 顶部返回栏的参数
interface BackHeaderProps {
  onBack: () => void // 点击返回触发的回调，回退目标与副作用由各页面决定
  title?: ReactNode // 居中显示的标题，缺省则不渲染标题只留返回按钮
  ariaLabel?: string // 返回按钮的可访问名称，默认「返回」
}

// BackHeader 顶部返回栏：左侧圆形返回键 + 可选居中标题，视觉复用 .sub-top 样式
export default function BackHeader({ onBack, title, ariaLabel = '返回' }: BackHeaderProps) {
  return (
    <header className="sub-top">
      <button className="back" type="button" aria-label={ariaLabel} onClick={onBack}>
        <svg
          width="18"
          height="18"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <path d="M15 18l-6-6 6-6" />
        </svg>
      </button>
      {title ? <h1 className="title">{title}</h1> : null}
    </header>
  )
}
