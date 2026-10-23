import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { Eye, EyeOff, Loader2, Zap } from 'lucide-react'
import { ApiError } from '@/lib/api'
import { useAuth } from '@/components/providers/AuthProvider'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Input'

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // 登录后回到用户原本想去的页面，而不是一律回仪表盘。
  // 只接受站内相对路径，避免被构造成外站跳转。
  const from = (location.state as { from?: string } | null)?.from
  const redirect = from && from.startsWith('/') && !from.startsWith('//') ? from : '/'

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      await login(username, password)
      navigate(redirect, { replace: true })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '登录失败，请稍后重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden px-4 py-10">
      {/* 背景大字：只有装饰，被面板压住一半，形成纵深 */}
      <div
        className="pointer-events-none absolute inset-0 flex items-center justify-center"
        aria-hidden="true"
      >
        <span className="select-none font-display text-[26vw] font-bold leading-none tracking-tighter text-primary/[0.06]">
          WG
        </span>
      </div>

      <div className="relative w-full max-w-sm">
        {/* 品牌区 */}
        <div className="mb-6">
          <div className="mb-4 flex items-center gap-3">
            <div className="relative border border-primary/60 bg-primary/10 p-2.5 [clip-path:polygon(8px_0,100%_0,100%_calc(100%_-_8px),calc(100%_-_8px)_100%,0_100%,0_8px)]">
              <Zap className="h-6 w-6 text-primary" aria-hidden="true" />
            </div>
            <div>
              <h1
                className="glitch font-display text-2xl font-bold uppercase tracking-[0.16em] text-primary"
                data-text="WGPanel"
              >
                WGPanel
              </h1>
              <p className="mt-1 font-mono text-[10px] uppercase tracking-[0.24em] text-muted-foreground">
                WireGuard Control Plane
              </p>
            </div>
          </div>

          {/* 警示条纹分隔 */}
          <div className="hazard-stripes h-1 w-full opacity-70" aria-hidden="true" />
        </div>

        {/* 登录面板 */}
        <form
          onSubmit={handleSubmit}
          className="glass-card hud-corners relative space-y-4 p-6 pt-7"
        >
          <p className="hud-label text-primary">// 接入终端</p>

          <Field label="用户名" htmlFor="username">
            <Input
              id="username"
              name="username"
              autoComplete="username"
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="admin"
              autoFocus
            />
          </Field>

          <Field label="密码" htmlFor="password">
            <div className="relative">
              <Input
                id="password"
                name="password"
                type={showPassword ? 'text' : 'password'}
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="pr-10"
              />
              <button
                type="button"
                onClick={() => setShowPassword((v) => !v)}
                aria-label={showPassword ? '隐藏密码' : '显示密码'}
                className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-muted-foreground transition-colors hover:text-primary"
              >
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </Field>

          {error && (
            <div
              role="alert"
              className="flex items-start gap-2 border border-destructive/50 bg-destructive/10 px-3 py-2 text-xs text-destructive [clip-path:polygon(6px_0,100%_0,100%_calc(100%_-_6px),calc(100%_-_6px)_100%,0_100%,0_6px)]"
            >
              <span className="hud-label shrink-0 pt-0.5 text-destructive">ERR</span>
              <span className="break-words">{error}</span>
            </div>
          )}

          <Button type="submit" size="lg" className="w-full" disabled={submitting}>
            {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
            {submitting ? '接入中…' : '接入系统'}
          </Button>

          <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
            首次使用请查看容器启动日志中的初始管理员密码
          </p>
        </form>
      </div>
    </div>
  )
}
