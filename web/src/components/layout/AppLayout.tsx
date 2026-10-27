import { useEffect, useMemo, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import {
  Activity,
  Cpu,
  LayoutDashboard,
  LogOut,
  Menu,
  Network,
  ScrollText,
  Server,
  Settings,
  UserCog,
  Users,
  X,
  Zap,
} from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { roleCan } from '@/lib/permissions'
import { useAuth } from '@/components/providers/AuthProvider'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import type { Me, Permission } from '@/types/api'

interface NavItem {
  to: string
  label: string
  /** 侧栏里的英文代号，2077 的 HUD 风格靠这种短标签建立纵深。 */
  code: string
  icon: typeof LayoutDashboard
  /**
   * 需要的最低权限；无权用户直接看不到入口，而不是点进去再被拦。
   * 省略表示登录即可访问——账号页属于这一类。
   */
  permission?: Permission
}

const NAV_ITEMS: NavItem[] = [
  { to: '/', label: '仪表盘', code: 'OVR', icon: LayoutDashboard, permission: 'metrics:read' },
  { to: '/peers', label: '客户端', code: 'NODES', icon: Users, permission: 'peer:read' },
  { to: '/monitor', label: '实时监控', code: 'LIVE', icon: Activity, permission: 'metrics:read' },
  { to: '/settings', label: '服务端设置', code: 'CONF', icon: Settings, permission: 'server:read' },
  { to: '/account', label: '账号', code: 'AUTH', icon: UserCog },
  { to: '/audit', label: '审计日志', code: 'LOG', icon: ScrollText, permission: 'audit:read' },
  { to: '/system', label: '系统运维', code: 'SYS', icon: Server, permission: 'settings:write' },
]

/** 部署网络模式的中文标签，与后端 netdriver.Mode 一一对应。 */
const NET_MODE_LABELS: Record<string, string> = {
  bridge: '桥接网络模式',
  host: 'Host 网络模式',
  ipvlan: 'IPvlan 网络模式',
  macvlan: 'Macvlan 网络模式',
}

export function AppLayout() {
  const { user, logout } = useAuth()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const location = useLocation()
  const navigate = useNavigate()

  // 路由变化时收起移动端抽屉。必须依赖 location 而不是抽屉自身的状态：
  // 依赖 drawerOpen 会导致「打开→立即关闭」的自锁，且点击链接后抽屉不会收起。
  useEffect(() => setDrawerOpen(false), [location.pathname])

  // Esc 关闭抽屉：抽屉打开时键盘用户也能退出，不需要先点遮罩。
  useEffect(() => {
    if (!drawerOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrawerOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [drawerOpen])

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  const versionQuery = useQuery({
    queryKey: ['version'],
    queryFn: ({ signal }) => api.version(signal),
    staleTime: 60 * 60 * 1000,
    retry: false,
  })

  const items = useMemo(
    () => NAV_ITEMS.filter((item) => !item.permission || roleCan(user?.role, item.permission)),
    [user?.role],
  )

  const current = items.find((i) =>
    i.to === '/' ? location.pathname === '/' : location.pathname.startsWith(i.to),
  )

  return (
    <div className="flex min-h-full">
      {/* 桌面端固定侧边栏 */}
      <aside className="hidden w-64 shrink-0 flex-col border-r border-border bg-card/60 backdrop-blur-sm lg:flex">
        <SidebarContent
          user={user}
          version={versionQuery.data?.version}
          items={items}
          onLogout={handleLogout}
        />
      </aside>

      {/* 移动端抽屉：点击遮罩或按 Esc 关闭 */}
      {drawerOpen && (
        <div className="fixed inset-0 z-50 lg:hidden">
          <button
            type="button"
            aria-label="关闭菜单"
            onClick={() => setDrawerOpen(false)}
            className="absolute inset-0 bg-background/80 backdrop-blur-sm"
          />
          <aside className="animate-slide-in-left absolute inset-y-0 left-0 flex w-72 flex-col border-r border-border bg-card">
            <SidebarContent
              user={user}
              version={versionQuery.data?.version}
              items={items}
              onLogout={handleLogout}
              onClose={() => setDrawerOpen(false)}
            />
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-40 border-b border-border bg-background/85 backdrop-blur-md">
          <div className="flex h-14 items-center gap-3 px-4 sm:px-6">
            <Button
              variant="ghost"
              size="icon"
              className="lg:hidden"
              aria-label="打开菜单"
              aria-expanded={drawerOpen}
              onClick={() => setDrawerOpen(true)}
            >
              <Menu className="h-5 w-5" />
            </Button>

            <div className="flex items-center gap-2 lg:hidden">
              <Zap className="h-4 w-4 text-primary" />
              <span className="font-display text-sm font-bold uppercase tracking-[0.16em]">WGPanel</span>
            </div>

            {/* 当前模块的 HUD 面包屑：代号 + 中文名 */}
            <div className="hidden items-center gap-2 lg:flex">
              <span className="hud-label text-primary">// {current?.code ?? 'SYS'}</span>
              <span className="text-sm text-muted-foreground">{current?.label ?? '控制台'}</span>
            </div>

            <div className="ml-auto flex items-center gap-2">
              <Badge variant="outline" className="hidden font-mono sm:inline-flex">
                <span className="text-primary">{user?.username}</span>
                <span className="text-muted-foreground">/</span>
                {user?.role}
              </Badge>
              <Button variant="ghost" size="icon" aria-label="退出登录" onClick={handleLogout}>
                <LogOut className="h-4 w-4" />
              </Button>
            </div>
          </div>

          {/* 顶栏底缘的警示条纹 + 一道横向扫描光带 */}
          <div className="relative h-[2px] w-full overflow-hidden">
            <div className="hazard-stripes absolute inset-0 opacity-25" aria-hidden="true" />
            <div
              className="absolute inset-y-0 w-1/4 animate-sweep bg-gradient-to-r from-transparent via-primary to-transparent"
              aria-hidden="true"
            />
          </div>
        </header>

        <main className="flex-1 px-4 py-5 sm:px-6 sm:py-6">
          <div className="container">
            <Outlet />
          </div>
        </main>

        <footer className="relative border-t border-border/60 px-4 py-3 text-center sm:px-6">
          <p className="font-mono text-[11px] uppercase tracking-[0.2em] text-muted-foreground">
            WGPANEL // WIREGUARD CONTROL PLANE
          </p>
        </footer>
      </div>
    </div>
  )
}

function SidebarContent({
  user,
  version,
  items,
  onLogout,
  onClose,
}: {
  user: Pick<Me, 'username' | 'role' | 'netMode'> | null
  version?: string
  items: NavItem[]
  onLogout: () => void
  onClose?: () => void
}) {
  return (
    <>
      <div className="flex h-14 items-center gap-2 border-b border-border/60 px-4">
        <Zap className="h-5 w-5 text-primary" aria-hidden="true" />
        <span
          className="glitch font-display text-base font-bold uppercase tracking-[0.18em] text-primary"
          data-text="WGPanel"
        >
          WGPanel
        </span>
        <span className="font-mono text-[10px] text-muted-foreground">
          v{version ? version.replace(/^v/, '') : '—'}
        </span>
        {onClose && (
          <Button variant="ghost" size="icon-sm" className="ml-auto" onClick={onClose} aria-label="关闭">
            <X className="h-4 w-4" />
          </Button>
        )}
      </div>

      <nav className="flex-1 space-y-1 p-3">
        {items.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === '/'}
            className={({ isActive }) =>
              cn(
                'group relative flex items-center gap-3 px-3 py-2.5 font-display text-xs font-semibold uppercase tracking-[0.12em] transition-colors',
                '[clip-path:polygon(6px_0,100%_0,100%_calc(100%_-_6px),calc(100%_-_6px)_100%,0_100%,0_6px)]',
                isActive
                  ? 'bg-primary/15 text-primary'
                  : 'text-muted-foreground hover:bg-accent hover:text-foreground',
              )
            }
          >
            {({ isActive }) => (
              <>
                {/* 选中项左侧的实心黄条，是侧栏里最强的状态信号 */}
                <span
                  className={cn(
                    'absolute inset-y-0 left-0 w-[3px] transition-colors',
                    isActive ? 'bg-primary shadow-[0_0_10px_hsl(var(--primary)/0.9)]' : 'bg-transparent',
                  )}
                  aria-hidden="true"
                />
                <item.icon
                  className={cn(
                    'h-4 w-4 shrink-0 transition-colors',
                    isActive ? 'text-primary' : 'text-muted-foreground group-hover:text-foreground',
                  )}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1 truncate">{item.label}</span>
                <span className="font-mono text-[9px] tracking-[0.1em] text-muted-foreground/70">
                  {item.code}
                </span>
              </>
            )}
          </NavLink>
        ))}
      </nav>

      <div className="space-y-3 border-t border-border/60 p-3">
        <div className="border border-border/70 bg-background/60 p-3">
          <div className="flex items-center justify-between gap-2">
            <p className="truncate font-display text-xs font-semibold uppercase tracking-[0.12em] text-primary">
              {user?.username}
            </p>
            <span className="h-1.5 w-1.5 bg-success shadow-[0_0_8px_hsl(var(--success)/0.9)]" />
          </div>
          <p className="mt-1 font-mono text-[11px] text-muted-foreground">角色：{user?.role}</p>
        </div>

        <dl className="space-y-1.5 font-mono text-[11px] text-muted-foreground">
          <div className="flex items-center gap-2">
            <Activity className="h-3.5 w-3.5 text-primary/70" />
            <dd>WireGuard 控制面</dd>
          </div>
          <div className="flex items-center gap-2">
            <Network className="h-3.5 w-3.5 text-primary/70" />
            <dd>
              {user?.netMode
                ? (NET_MODE_LABELS[user.netMode] ?? `${user.netMode} 网络模式`)
                : '桥接网络模式'}
            </dd>
          </div>
          <div className="flex items-center gap-2">
            <Cpu className="h-3.5 w-3.5 text-primary/70" />
            <dd>数据保存在本地 SQLite</dd>
          </div>
        </dl>

        <div className="hazard-stripes h-1 w-full opacity-40" aria-hidden="true" />

        {onLogout && (
          <Button variant="outline" size="sm" className="w-full" onClick={onLogout}>
            <LogOut className="h-3.5 w-3.5" />
            退出登录
          </Button>
        )}
      </div>
    </>
  )
}
