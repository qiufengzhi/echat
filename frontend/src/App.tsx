import { Navigate, Outlet, Route, Routes } from 'react-router-dom'

import AppLayout from './components/layout/AppLayout'
import { isAuthed } from './services/auth'
import LegacyFlow from './pages/LegacyFlow'
import LoginPage from './pages/auth/LoginPage'
import RegisterPage from './pages/auth/RegisterPage'
import ResetPasswordPage from './pages/auth/ResetPasswordPage'

// RequireAuth 路由守卫：未持有 access 令牌时重定向到登录页，已登录才渲染子路由
function RequireAuth() {
  if (!isAuthed()) {
    return <Navigate to="/login" replace />
  }
  return <Outlet />
}

function App() {
  return (
    <Routes>
      <Route element={<AppLayout />}>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        <Route path="/forgot" element={<ResetPasswordPage />} />

        <Route element={<RequireAuth />}>
          <Route path="/" element={<LegacyFlow />} />
        </Route>

        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}

export default App