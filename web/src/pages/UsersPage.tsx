import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  KeyRound,
  Loader2,
  Plus,
  ShieldCheck,
  Trash2,
  UserCog,
} from 'lucide-react'
import { ApiError, api } from '@/lib/api'
import { canManageRole, roleLabel } from '@/lib/permissions'
import { passwordSatisfiesAll } from '@/lib/password'
import { formatRelative } from '@/lib/utils'
import { useAuth } from '@/components/providers/AuthProvider'
import { useToast } from '@/components/providers/ToastProvider'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/Card'
import { ConfirmDialog, Dialog } from '@/components/ui/Dialog'
import { PasswordRules } from '@/components/ui/PasswordRules'
import { Field, Input, Select } from '@/components/ui/Input'
import { EmptyState, TableSkeleton } from '@/components/ui/Skeleton'
import type { Role, User } from '@/types/api'

/**
 * 可分配的角色。owner 不在清单里：
 * 建号与改角色都不允许产出 owner，部署的根身份只在首次启动时生成。
 */
const ASSIGNABLE_ROLES: { value: Role; label: string; hint: string }[] = [
  { value: 'admin', label: '管理员', hint: '除账号管理外的全部权限' },
  { value: 'operator', label: '运维', hint: '客户端与服务端运维，无审计与设置' },
  { value: 'viewer', label: '只读', hint: '仅查看仪表盘与客户端' },
]

export function UsersPage() {
  const { user } = useAuth()
  const toast = useToast()
  const queryClient = useQueryClient()

  const [creating, setCreating] = useState(false)
  const [resetting, setResetting] = useState<User | null>(null)
  const [removing, setRemoving] = useState<User | null>(null)
  const [toggling, setToggling] = useState<User | null>(null)

  const usersQuery = useQuery({
    queryKey: ['users'],
    queryFn: ({ signal }) => api.listUsers(signal),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['users'] })

  const handleError = (err: unknown, fallback: string) => {
    toast.error(err instanceof ApiError ? err.message : fallback)
  }

  const toggleEnabled = useMutation({
    mutationFn: ({ target, enabled }: { target: User; enabled: boolean }) =>
      api.updateUser(target.id, { enabled }),
    onSuccess: (_data, vars) => {
      toast.success(vars.enabled ? `已启用 ${vars.target.username}` : `已停用 ${vars.target.username}，其全部会话已失效`)
      invalidate()
    },
    onError: (err) => handleError(err, '操作失败'),
  })

  const changeRole = useMutation({
    mutationFn: ({ target, role }: { target: User; role: Role }) =>
      api.updateUser(target.id, { role }),
    onSuccess: (_data, vars) => {
      toast.success(`${vars.target.username} 角色已调整为 ${roleLabel(vars.role)}`)
      invalidate()
    },
    onError: (err) => handleError(err, '角色调整失败'),
  })

  const users = usersQuery.data ?? []
  const selfId = user?.userId
  const actorRole = user?.role

  return (
    <div className="space-y-5">
      <Card>
        <CardHeader className="flex-row items-start justify-between gap-4 space-y-0">
          <div className="space-y-1.5">
            <CardDescription className="hud-label">// AUTH-05</CardDescription>
            <h1 className="font-display text-xl font-bold uppercase tracking-[0.08em]">账号管理</h1>
            <p className="text-sm text-muted-foreground">
              管理面板登录账号与角色分配。停用与重置密码会立即吊销目标账号的全部会话。
            </p>
          </div>
          <Button onClick={() => setCreating(true)}>
            <Plus className="h-4 w-4" />
            新建账号
          </Button>
        </CardHeader>
        <CardContent>
          {usersQuery.isLoading ? (
            <TableSkeleton rows={4} />
          ) : usersQuery.isError ? (
            <EmptyState
              icon={ShieldCheck}
              title="ERR // 拒绝访问"
              description="账号列表加载失败，请稍后重试。"
            />
          ) : users.length === 0 ? (
            <EmptyState
              icon={UserCog}
              title="NO ACCOUNTS"
              description="还没有任何账号。"
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-left font-mono text-[10px] uppercase tracking-[0.14em] text-muted-foreground">
                    <th className="py-2.5 pr-4 font-medium">账号</th>
                    <th className="py-2.5 pr-4 font-medium">角色</th>
                    <th className="py-2.5 pr-4 font-medium">状态</th>
                    <th className="py-2.5 pr-4 font-medium">最近登录</th>
                    <th className="py-2.5 pr-4 font-medium">创建时间</th>
                    <th className="py-2.5 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {users.map((u) => {
                    const isSelf = u.id === selfId
                    // 与后端 canManage 同构：管理自己的账号在业务上被禁止，
                    // 层级不够的（如 admin 对 admin/owner）也不给可点控件，
                    // 避免「按钮可点、点了必 403」的错误暗示。
                    const manageable = !isSelf && canManageRole(actorRole, u.role)
                    const canReset = isSelf || canManageRole(actorRole, u.role)
                    return (
                      <tr key={u.id} className="border-b border-border/50 last:border-0">
                        <td className="py-3 pr-4">
                          <div className="flex items-center gap-2">
                            <span className="font-medium text-foreground">{u.username}</span>
                            {isSelf && (
                              <Badge variant="outline" className="font-mono text-[10px]">当前</Badge>
                            )}
                          </div>
                          {u.email && (
                            <p className="mt-0.5 text-xs text-muted-foreground">{u.email}</p>
                          )}
                        </td>
                        <td className="py-3 pr-4">
                          {manageable ? (
                            <Select
                              aria-label={`${u.username} 的角色`}
                              className="h-8 w-32"
                              value={u.role}
                              disabled={changeRole.isPending}
                              onChange={(e) =>
                                changeRole.mutate({ target: u, role: e.target.value as Role })
                              }
                            >
                              {ASSIGNABLE_ROLES.filter((r) => canManageRole(actorRole, r.value)).map((r) => (
                                <option key={r.value} value={r.value}>{r.label}</option>
                              ))}
                              {/* owner 行保持显示原角色；降级由后端再校验「最后 owner」守卫。 */}
                              {u.role === 'owner' && <option value="owner">所有者</option>}
                            </Select>
                          ) : (
                            <Badge variant="outline">{roleLabel(u.role)}</Badge>
                          )}
                        </td>
                        <td className="py-3 pr-4">
                          <Badge variant={u.enabled ? 'success' : 'muted'}>
                            {u.enabled ? '启用' : '停用'}
                          </Badge>
                        </td>
                        <td className="py-3 pr-4 font-mono text-xs text-muted-foreground">
                          {u.lastLoginAt ? formatRelative(u.lastLoginAt) : '—'}
                        </td>
                        <td className="py-3 pr-4 font-mono text-xs text-muted-foreground">
                          {formatRelative(u.createdAt)}
                        </td>
                        <td className="py-3 text-right">
                          <div className="inline-flex items-center gap-1.5">
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={!manageable || toggleEnabled.isPending}
                              title={isSelf ? '不能停用自己的账号' : !manageable ? '权限不足以管理该账号' : undefined}
                              onClick={() => setToggling(u)}
                            >
                              {u.enabled ? '停用' : '启用'}
                            </Button>
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={!canReset}
                              title={!canReset ? '权限不足以管理该账号' : undefined}
                              onClick={() => setResetting(u)}
                            >
                              <KeyRound className="h-3.5 w-3.5" />
                              重置密码
                            </Button>
                            <Button
                              variant="destructive"
                              size="sm"
                              disabled={!manageable || u.role === 'owner'}
                              title={isSelf
                                ? '不能删除自己的账号'
                                : u.role === 'owner'
                                  ? '部署根身份不可删除'
                                  : !manageable
                                    ? '权限不足以管理该账号'
                                    : undefined}
                              onClick={() => setRemoving(u)}
                            >
                              <Trash2 className="h-3.5 w-3.5" />
                              删除
                            </Button>
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      <CreateUserDialog
        open={creating}
        actorRole={actorRole}
        onClose={() => setCreating(false)}
        onCreated={() => {
          setCreating(false)
          invalidate()
        }}
        onError={handleError}
      />

      <ResetPasswordDialog
        target={resetting}
        onClose={() => setResetting(null)}
        onDone={() => {
          setResetting(null)
          invalidate()
        }}
        onError={handleError}
      />

      <ConfirmDialog
        open={removing !== null}
        title="删除账号"
        message={`确定删除 ${removing?.username ?? ''} 吗？该账号的全部会话将立即失效，操作会记入审计日志。`}
        confirmText="删除"
        submitting={false}
        onClose={() => setRemoving(null)}
        onConfirm={async () => {
          if (!removing) return
          try {
            await api.deleteUser(removing.id)
            toast.success(`已删除 ${removing.username}`)
            setRemoving(null)
            invalidate()
          } catch (err) {
            handleError(err, '删除失败')
            setRemoving(null)
          }
        }}
      />

      {/*
        停用/启用需要确认。
        停用会立即踢掉目标账号的全部会话，是有代价的不可逆操作：
        点错一次，对方当场掉线且没有任何提示。
        同页的删除与重置密码都有确认框，停用不该成为唯一的例外。
      */}
      <ConfirmDialog
        open={toggling !== null}
        title={toggling?.enabled ? '停用账号' : '启用账号'}
        message={
          toggling?.enabled
            ? `确定停用 ${toggling.username} 吗？该账号将立即无法登录，其全部会话会失效，操作会记入审计日志。`
            : `确定启用 ${toggling?.username ?? ''} 吗？该账号将可以重新登录。`
        }
        confirmText={toggling?.enabled ? '停用' : '启用'}
        destructive={toggling?.enabled ?? true}
        submitting={toggleEnabled.isPending}
        onClose={() => setToggling(null)}
        onConfirm={() => {
          if (!toggling) return
          // 用 mutate 而非 mutateAsync：失败提示统一由 mutation 的 onError 给出，
          // 避免这里再 catch 一次导致同一条错误弹两遍。
          toggleEnabled.mutate({ target: toggling, enabled: !toggling.enabled })
          setToggling(null)
        }}
      />
    </div>
  )
}

function CreateUserDialog({
  open,
  actorRole,
  onClose,
  onCreated,
  onError,
}: {
  open: boolean
  actorRole: Role | undefined
  onClose: () => void
  onCreated: () => void
  onError: (err: unknown, fallback: string) => void
}) {
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<Role>('viewer')
  const [submitting, setSubmitting] = useState(false)

  const reset = () => {
    setUsername('')
    setEmail('')
    setPassword('')
    setRole('viewer')
  }

  const submit = async () => {
    setSubmitting(true)
    try {
      await api.createUser({ username: username.trim(), email: email.trim(), password, role })
      onCreated()
      reset()
    } catch (err) {
      onError(err, '创建失败')
    } finally {
      setSubmitting(false)
    }
  }

  // 规则不满足时禁用提交，避免用户提交后才被后端拒绝。
  const rulesOk = passwordSatisfiesAll(password, username.trim())

  return (
    <Dialog
      open={open}
      title="新建账号"
      description="密码至少 10 位，需混合字母与数字或符号。"
      onClose={() => {
        onClose()
        reset()
      }}
      footer={
        <>
          <Button variant="outline" onClick={() => { onClose(); reset() }}>取消</Button>
          <Button onClick={submit} disabled={submitting || !username.trim() || !password || !rulesOk}>
            {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
            创建
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="用户名" hint="3-32 位，字母、数字、_ . -（不区分大小写，统一存为小写）">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </Field>
        <Field label="邮箱（可选）">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Field label="初始密码">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          <PasswordRules password={password} username={username} />
        </Field>
        <Field label="角色">
          <Select value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {/* 只列当前操作者有权创建的角色：admin 看不到「管理员」，
                否则选中后必然被后端 403 拒绝。 */}
            {ASSIGNABLE_ROLES.filter((r) => canManageRole(actorRole, r.value)).map((r) => (
              <option key={r.value} value={r.value}>{r.label}（{r.hint}）</option>
            ))}
          </Select>
        </Field>
      </div>
    </Dialog>
  )
}

function ResetPasswordDialog({
  target,
  onClose,
  onDone,
  onError,
}: {
  target: User | null
  onClose: () => void
  onDone: () => void
  onError: (err: unknown, fallback: string) => void
}) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const reset = () => {
    setPassword('')
    setConfirm('')
  }

  const mismatch = confirm !== '' && password !== confirm
  // 规则不满足时直接禁用提交：让用户在按钮处就知道哪里不合格，
  // 而不是提交后被后端拒绝再回来改。
  const rulesOk = passwordSatisfiesAll(password, target?.username)
  const canSubmit = password !== '' && password === confirm && rulesOk && !submitting

  const submit = async () => {
    if (!target) return
    setSubmitting(true)
    try {
      await api.resetUserPassword(target.id, password)
      onDone()
      reset()
    } catch (err) {
      onError(err, '重置失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={target !== null}
      title={`重置 ${target?.username ?? ''} 的密码`}
      description="重置后该账号的全部会话立即失效，需用新密码重新登录。"
      onClose={() => { onClose(); reset() }}
      footer={
        <>
          <Button variant="outline" onClick={() => { onClose(); reset() }}>取消</Button>
          <Button onClick={submit} disabled={!canSubmit}>
            {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
            重置
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="新密码" hint="至少 10 位，需混合字母与数字或符号">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
          <PasswordRules password={password} username={target?.username} />
        </Field>
        <Field label="确认新密码" error={mismatch ? '两次输入不一致' : undefined}>
          <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        </Field>
      </div>
    </Dialog>
  )
}
