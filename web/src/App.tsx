import { lazy, Suspense } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Loader2 } from 'lucide-react'
import { roleCan } from '@/lib/permissions'
import { useAuth } from '@/components/providers/AuthProvider'
import { AppLayout } from '@/components/layout/AppLayout'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { LoginPage } from '@/pages/LoginPage'
import { DashboardPage } from '@/pages/DashboardPage'
import { NotFoundPage } from '@/pages/NotFoundPage'
import type { Permission } from '@/types/api'

// 路由级懒加载：监控/系统/审计等页面只在真正访问时才下载，
// 首屏只需要仪表盘与登录相关的代码。
const PeersPage = lazy(() => import('@/pages/PeersPage').then((m) => ({ default: m.PeersPage })))
const MonitorPage = lazy(() => import('@/pages/MonitorPage').then((m) => ({ default: m.MonitorPage })))
const SettingsPage = lazy(() => import('@/pages/SettingsPage').then((m) => ({ default: m.SettingsPage })))
const AccountPage = lazy(() => import('@/pages/AccountPage').then((m) => ({ default: m.AccountPage })))
const AuditPage = lazy(() => import('@/pages/AuditPage').then((m) => ({ default: m.AuditPage })))
const SystemPage = lazy(() => import('@/pages/SystemPage').then((m) => ({ default: m.SystemPage })))

/** 全屏加载态，避免路由守卫与懒加载期间闪现空页面。 */
function FullScreenLoader() {
  return (
    <div className="flex min-h-full items-center justify-center">
      <Loader2 className="h-6 w-6 animate-spin text-primary" aria-label="加载中" />
    </div>
  )
}

/** 要求已登录，否则跳转登录页并记住原始位置。 */
function RequireAuth({ children }: { children: React.ReactNode }) {
  const { user, loading } = useAuth()
  const location = useLocation()

  if (loading) return <FullScreenLoader />
  if (!user) return <Navigate to="/login" state={{ from: location.pathname }} replace />
  return <>{children}</>
}

/**
 * 权限守卫。前端仅用于体验层（避免无权用户看到不可用按钮），
 * 真正的授权判定在后端中间件，前端被绕过也不会导致越权。
 *
 * permission 省略表示「登录即可访问」：账号页属于这一类，
 * 它只操作当前登录者自己的凭据，不涉及任何角色权限点。
 */
function RequirePermission({
  permission,
  children,
}: {
  permission?: Permission
  children: React.ReactNode
}) {
  const { user } = useAuth()
  if (permission && !roleCan(user?.role, permission)) {
    return (
      <div className="glass-card p-8 text-center">
        <p className="font-medium">没有访问权限</p>
        <p className="mt-1 text-sm text-muted-foreground">
          当前角色无权查看此页面，请联系管理员调整角色。
        </p>
      </div>
    )
  }
  return <>{children}</>
}

/** 把守卫、懒加载与错误边界组合成一处声明，避免每个路由重复三层嵌套。 */
function Guarded({
  permission,
  children,
}: {
  permission?: Permission
  children: React.ReactNode
}) {
  return (
    <RequirePermission permission={permission}>
      <ErrorBoundary>
        <Suspense fallback={<FullScreenLoader />}>{children}</Suspense>
      </ErrorBoundary>
    </RequirePermission>
  )
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAuth>
            <AppLayout />
          </RequireAuth>
        }
      >
        <Route index element={<DashboardPage />} />
        <Route
          path="peers"
          element={
            <Guarded permission="peer:read">
              <PeersPage />
            </Guarded>
          }
        />
        <Route
          path="monitor"
          element={
            <Guarded permission="metrics:read">
              <MonitorPage />
            </Guarded>
          }
        />
        <Route
          path="settings"
          element={
            <Guarded permission="server:read">
              <SettingsPage />
            </Guarded>
          }
        />
        <Route
          path="account"
          element={
            <Guarded>
              <AccountPage />
            </Guarded>
          }
        />
        <Route
          path="audit"
          element={
            <Guarded permission="audit:read">
              <AuditPage />
            </Guarded>
          }
        />
        <Route
          path="system"
          element={
            <Guarded permission="settings:write">
              <SystemPage />
            </Guarded>
          }
        />
        {/* 其余路径在已登录壳内也走 404，而不是静默回仪表盘。 */}
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  )
}