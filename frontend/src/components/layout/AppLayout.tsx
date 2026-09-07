import { Outlet } from 'react-router-dom'

// AppLayout 是全局移动壳容器：宽度 430px 居中、撑满视口高度、白底。
// 桌面端即窄栏居中（两侧露出 #EFE6F2 背景），所有页面以 <Outlet/> 渲染在壳内
export default function AppLayout() {
  return (
    <div className="app-layout">
      <Outlet />
    </div>
  )
}
