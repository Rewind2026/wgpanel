import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Eye, EyeOff, KeyRound, Loader2, UserCog } from 'lucide-react'
import { ApiError, api } from '@/lib/api'
import { roleLabel } from '@/lib/permissions'
import { formatRelative, formatTime } from '@/lib/utils'
import { useToast } from '@/components/providers/ToastProvider'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/Card'
import { Field, Input } from '@/components/ui/Input'

/**
 * 账号页：单人部署下的「我自己」。
 *
 * 面板不再提供建号、删号与角色分配，这里只剩两件事——
 * 看一眼当前账号是谁，以及改自己的密码。
 * 改密之后全部会话立即失效（含当前浏览器），所以成功后直接回登录页。
 */
export function AccountPage() {
  const toast = useToast()

  const profileQuery = useQuery({
    queryKey: ['profile'],
    queryFn: ({ signal }) => api.getProfile(signal),
  })

  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [localError, setLocalError] = useState('')

  const changeMutation = useMutation({
    mutationFn: () => api.changePassword(password),
    onSuccess: () => {
      // 改密会吊销全部会话，当前 access token 也不再有效，
      // 留在页面上只会立刻撞上 401 再被踢走——主动回登录页更干净。
      toast.success('密码已修改，请使用新密码重新登录')
      setPassword('')
      setConfirm('')
      window.location.assign('/login')
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setLocalError('')
    if (password !== confirm) {
      setLocalError('两次输入的密码不一致')
      return
    }
    changeMutation.mutate()
  }

  const profile = profileQuery.data

  return (
    <div className="space-y-5">
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// AUTH-04</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              账号
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">
              当前登录账号的资料与密码维护
            </p>
          </div>
        </div>
        <div
          className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80"
          aria-hidden="true"
        />
      </header>

      <Card>
        <CardHeader>
          <CardTitle>当前账号</CardTitle>
          <CardDescription>面板为单人部署，此处展示的即唯一的管理账号</CardDescription>
        </CardHeader>
        <CardContent>
          {profileQuery.isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : profileQuery.isError || !profile ? (
            <p className="text-sm text-destructive">
              无法加载账号资料：{(profileQuery.error as ApiError)?.message}
            </p>
          ) : (
            <dl className="grid gap-4 sm:grid-cols-2">
              <div>
                <dt className="font-display text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                  用户名
                </dt>
                <dd className="mt-1 flex items-center gap-2">
                  <UserCog className="h-4 w-4 text-primary" aria-hidden="true" />
                  <span className="font-display text-sm font-semibold tracking-wide">
                    {profile.username}
                  </span>
                </dd>
              </div>
              <div>
                <dt className="font-display text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                  角色
                </dt>
                <dd className="mt-1">
                  <Badge variant="default">{roleLabel(profile.role)}</Badge>
                </dd>
              </div>
              <div>
                <dt className="font-display text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                  最近登录
                </dt>
                <dd className="mt-1 font-mono text-xs text-muted-foreground">
                  {profile.lastLoginAt ? formatRelative(profile.lastLoginAt) : '从未'}
                </dd>
              </div>
              <div>
                <dt className="font-display text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
                  创建时间
                </dt>
                <dd className="mt-1 font-mono text-xs text-muted-foreground">
                  {formatTime(profile.createdAt)}
                </dd>
              </div>
            </dl>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>修改密码</CardTitle>
          <CardDescription>
            修改后当前全部会话立即失效，需要用新密码重新登录
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="max-w-md space-y-4" onSubmit={submit}>
            <Field
              label="新密码"
              htmlFor="new-password"
              hint="至少 10 位，需混合字母与数字或符号"
              error={localError || undefined}
            >
              <div className="relative">
                <Input
                  id="new-password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="new-password"
                  required
                  minLength={10}
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

            <Field label="确认新密码" htmlFor="confirm-password">
              <Input
                id="confirm-password"
                type={showPassword ? 'text' : 'password'}
                autoComplete="new-password"
                required
                minLength={10}
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
            </Field>

            <Button type="submit" disabled={changeMutation.isPending}>
              {changeMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <KeyRound className="h-4 w-4" />
              )}
              更新密码
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
