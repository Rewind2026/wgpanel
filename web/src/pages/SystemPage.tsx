import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  DatabaseBackup,
  Download,
  HardDriveDownload,
  Loader2,
  RotateCcw,
  Save,
  Trash2,
  Upload,
} from 'lucide-react'
import { ApiError, api } from '@/lib/api'
import { roleCan } from '@/lib/permissions'
import { cn, formatBytes, formatTime } from '@/lib/utils'
import { useAuth } from '@/components/providers/AuthProvider'
import { useToast } from '@/components/providers/ToastProvider'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/Card'
import { ConfirmDialog } from '@/components/ui/Dialog'
import { Field, Input, Select } from '@/components/ui/Input'
import { EmptyState, TableSkeleton } from '@/components/ui/Skeleton'
import type { BackupRecord, NetworkInfo, RAStatus, Settings } from '@/types/api'

export function SystemPage() {
  const { user } = useAuth()
  const canWrite = roleCan(user?.role, 'settings:write')

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: ({ signal }) => api.getSettings(signal),
  })
  const versionQuery = useQuery({
    queryKey: ['version'],
    queryFn: ({ signal }) => api.version(signal),
    staleTime: Infinity,
  })

  if (settingsQuery.isLoading) {
    return (
      <div className="space-y-5">
        <div className="h-8 w-48" />
        <TableSkeleton rows={6} />
      </div>
    )
  }

  if (settingsQuery.isError || !settingsQuery.data) {
    return (
      <Card>
        <CardContent className="p-6 text-sm text-destructive">
          无法加载系统设置：{(settingsQuery.error as ApiError)?.message}
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-5">
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// SYS-07</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              系统与运维
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">
              运行时策略、加密备份与版本信息
            </p>
          </div>
          <Badge variant="outline" className="font-mono">
            v{versionQuery.data?.version ?? '—'}
          </Badge>
        </div>
        <div
          className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80"
          aria-hidden="true"
        />
      </header>

      <SettingsCard
        key={settingsQuery.data.updatedAt}
        initial={settingsQuery.data}
        readOnly={!canWrite}
      />

      {canWrite && <NetworkCard />}

      <BackupCard />

      <Card>
        <CardHeader>
          <CardTitle>关于</CardTitle>
          <CardDescription>版本与部署信息</CardDescription>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            <InfoRow label="版本" value={versionQuery.data?.version ?? '—'} mono />
            <InfoRow label="设置更新时间" value={formatTime(settingsQuery.data.updatedAt)} />
            <InfoRow
              label="备份目录"
              value={`${settingsQuery.data.backupDir}（数据目录内）`}
              mono
            />
            <InfoRow
              label="备份策略"
              value={
                settingsQuery.data.backupEnabled
                  ? `每 ${settingsQuery.data.backupIntervalHours} 小时，保留 ${settingsQuery.data.backupRetentionCount} 份`
                  : '已关闭'
              }
            />
          </dl>
        </CardContent>
      </Card>
    </div>
  )
}

function SettingsCard({ initial, readOnly }: { initial: Settings; readOnly: boolean }) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<Settings>(initial)

  const update = <K extends keyof Settings>(key: K, value: Settings[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }))

  const saveMutation = useMutation({
    mutationFn: () => api.updateSettings(form),
    onSuccess: (saved) => {
      toast.success('设置已保存，下一轮采集生效')
      queryClient.setQueryData(['settings'], saved)
      setForm(saved)
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const disabled = readOnly || saveMutation.isPending

  return (
    <Card>
      <CardHeader>
        <CardTitle>运行时策略</CardTitle>
        <CardDescription>
          保存后立即生效，不需要重启容器
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="采样间隔（秒）" htmlFor="s-sample" hint="1–10，越小越实时但数据库写入越频繁">
            <Input
              id="s-sample"
              type="number"
              min={1}
              max={10}
              value={form.sampleIntervalSeconds}
              disabled={disabled}
              onChange={(e) => update('sampleIntervalSeconds', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="调谐间隔（秒）" htmlFor="s-reconcile" hint="1–60，决定内核配置被修正的频率">
            <Input
              id="s-reconcile"
              type="number"
              min={1}
              max={60}
              value={form.reconcileIntervalSeconds}
              disabled={disabled}
              onChange={(e) => update('reconcileIntervalSeconds', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="到期预警（天）" htmlFor="s-warn" hint="0 表示不预警">
            <Input
              id="s-warn"
              type="number"
              min={0}
              max={90}
              value={form.expiryWarnDays}
              disabled={disabled}
              onChange={(e) => update('expiryWarnDays', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="公网出口地址" htmlFor="s-endpoint" hint="留空自动探测；显式设置可避免容器内探测拿到内网地址">
            <Input
              id="s-endpoint"
              value={form.publicEndpoint}
              disabled={disabled}
              onChange={(e) => update('publicEndpoint', e.target.value)}
              placeholder="vpn.example.com:51820"
              className="font-mono"
            />
          </Field>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <ToggleRow
            label="配额强制执行"
            hint="超量客户端自动停用，周期重置后自动恢复"
            checked={form.quotaEnforcement}
            disabled={disabled}
            onChange={(v) => update('quotaEnforcement', v)}
          />
          <ToggleRow
            label="到期强制执行"
            hint="到期客户端自动停用，需人工重新启用"
            checked={form.expiryEnforcement}
            disabled={disabled}
            onChange={(v) => update('expiryEnforcement', v)}
          />
          <ToggleRow
            label="自动备份"
            hint="按固定间隔生成 AES-256-GCM 加密备份"
            checked={form.backupEnabled}
            disabled={disabled}
            onChange={(v) => update('backupEnabled', v)}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="备份间隔（小时）" htmlFor="s-bkint" hint="1–720">
            <Input
              id="s-bkint"
              type="number"
              min={1}
              max={720}
              value={form.backupIntervalHours}
              disabled={disabled || !form.backupEnabled}
              onChange={(e) => update('backupIntervalHours', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="保留份数" htmlFor="s-bkkeep" hint="1–100，超出后删除最旧的">
            <Input
              id="s-bkkeep"
              type="number"
              min={1}
              max={100}
              value={form.backupRetentionCount}
              disabled={disabled || !form.backupEnabled}
              onChange={(e) => update('backupRetentionCount', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="备份子目录" htmlFor="s-bkdir" hint="只能是数据目录下的纯目录名">
            <Input
              id="s-bkdir"
              value={form.backupDir}
              disabled={disabled || !form.backupEnabled}
              onChange={(e) => update('backupDir', e.target.value)}
              className="font-mono"
            />
          </Field>
        </div>

        {!readOnly && (
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setForm(initial)} disabled={saveMutation.isPending}>
              <RotateCcw className="h-4 w-4" />
              撤销修改
            </Button>
            <Button onClick={() => saveMutation.mutate()} disabled={saveMutation.isPending}>
              {saveMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Save className="h-4 w-4" />
              )}
              保存设置
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/** 部署网络模式与 netdriver.Mode 对应的中文标签。 */
const NET_MODE_LABELS: Record<string, string> = {
  bridge: '桥接',
  host: 'Host',
  ipvlan: 'IPvlan',
  macvlan: 'Macvlan',
}

/** 数值型 RA 状态的展示文案；-1 表示内核值读取失败。 */
const sysctlText = (v: number) => (v < 0 ? '读取失败' : String(v))

/** IPv6 RA/SLAAC 链路状态行。ipvlan/macvlan 下这条链路决定外部能否经 v6 直连。 */
function RARow({ label, value, ok }: { label: string; value: string; ok: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 px-3 py-1.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className={cn('font-mono text-xs', ok ? 'text-success' : 'text-destructive')}>
        {value}
      </span>
    </div>
  )
}

/** 把 RA 状态浓缩成一行结论：链路健康、已自愈还是有阻断问题。 */
function raSummary(ra: RAStatus): { text: string; ok: boolean } {
  if (ra.problems?.length) {
    return { text: ra.problems[0], ok: false }
  }
  if (ra.applied?.length) {
    return { text: `已自愈：${ra.applied.join('、')}`, ok: true }
  }
  if (ra.globalV6.length > 0) {
    return { text: `SLAAC 正常（${ra.globalV6.length} 个全局地址）`, ok: true }
  }
  return { text: '等待 RA 下发', ok: true }
}

/** 网络形态诊断卡：模式、接口地址与上行接口的 RA 链路状态。 */
function NetworkCard() {
  const netQuery = useQuery({
    queryKey: ['network'],
    queryFn: ({ signal }) => api.getNetwork(signal),
    refetchInterval: 15_000,
  })

  if (netQuery.isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>网络</CardTitle>
          <CardDescription>部署网络形态与 IPv6 RA 链路</CardDescription>
        </CardHeader>
        <CardContent>
          <TableSkeleton rows={3} />
        </CardContent>
      </Card>
    )
  }
  if (netQuery.isError || !netQuery.data) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>网络</CardTitle>
          <CardDescription>部署网络形态与 IPv6 RA 链路</CardDescription>
        </CardHeader>
        <CardContent>
          <ErrorBox message={(netQuery.error as ApiError)?.message ?? '加载失败'} />
        </CardContent>
      </Card>
    )
  }

  const info: NetworkInfo = netQuery.data
  const ra = info.ra
  const uplink = info.interfaces.find((i) => i.name === info.uplink)
  const summary = raSummary(ra)

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle>网络</CardTitle>
            <CardDescription>部署网络形态与上行接口的 IPv6 RA 链路</CardDescription>
          </div>
          <Badge variant="outline" className="font-mono uppercase">
            {NET_MODE_LABELS[info.mode] ?? info.mode}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <InfoRow label="上行接口" value={info.uplink || '—'} mono />
          <InfoRow
            label="上行 IPv4"
            value={uplink?.v4.length ? uplink.v4.join('、') : '—'}
            mono
          />
          <InfoRow
            label="上行全局 IPv6（RA/SLAAC）"
            value={uplink?.v6Global.length ? uplink.v6Global.join('、') : '—'}
            mono
          />
          <InfoRow label="当前探测 Endpoint" value={info.endpoint || '（空，未生成）'} mono />
        </div>

        <div className="border border-border/70 bg-background/60">
          <div className="flex items-center justify-between gap-3 border-b border-border/60 px-3 py-2">
            <p className="font-display text-xs font-semibold uppercase tracking-[0.12em]">
              IPv6 RA 链路（{ra.interface}）
            </p>
            <Badge variant={summary.ok ? 'default' : 'destructive'}>{summary.ok ? '正常' : '异常'}</Badge>
          </div>
          <div className="grid sm:grid-cols-3">
            <RARow label="disable_ipv6" value={sysctlText(ra.disableIPv6)} ok={ra.disableIPv6 === 0} />
            <RARow label="accept_ra" value={sysctlText(ra.acceptRA)} ok={ra.acceptRA === 2} />
            <RARow label="forwarding" value={sysctlText(ra.forwarding)} ok={ra.forwarding > 0} />
          </div>
          <div className="border-t border-border/60 px-3 py-2">
            <p className={cn('text-xs', summary.ok ? 'text-muted-foreground' : 'text-destructive')}>
              {summary.text}
              {!ra.writable && '（sysctl 只读：修复需在 compose 的 sysctls/endpoint sysctls 中注入）'}
            </p>
            {ra.applied?.length ? (
              <p className="mt-1 text-xs text-muted-foreground">本次自愈：{ra.applied.join('、')}</p>
            ) : null}
            {(ra.problems ?? []).slice(summary.ok ? 0 : 1).map((p) => (
              <p key={p} className="mt-1 text-xs text-destructive">
                {p}
              </p>
            ))}
          </div>
        </div>

        <details className="border border-border/70 bg-background/60">
          <summary className="cursor-pointer px-3 py-2 font-mono text-xs text-muted-foreground">
            全部接口地址（{info.interfaces.length}）
          </summary>
          <div className="space-y-1 px-3 pb-3">
            {info.interfaces.map((it) => (
              <div key={it.name} className="flex flex-wrap gap-x-4 font-mono text-[11px]">
                <span className="text-primary">{it.name}</span>
                <span className="text-muted-foreground">
                  {it.addrs.length ? it.addrs.join('  ') : '（无地址）'}
                </span>
              </div>
            ))}
          </div>
        </details>
      </CardContent>
    </Card>
  )
}

function BackupCard() {
  const toast = useToast()
  const queryClient = useQueryClient()
  const [restoring, setRestoring] = useState<BackupRecord | null>(null)
  const [deleting, setDeleting] = useState<BackupRecord | null>(null)

  const backupsQuery = useQuery({
    queryKey: ['backups'],
    queryFn: ({ signal }) => api.listBackups(signal),
  })

  const createMutation = useMutation({
    mutationFn: () => api.createBackup(),
    onSuccess: () => {
      toast.success('备份已创建')
      queryClient.invalidateQueries({ queryKey: ['backups'] })
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const restoreMutation = useMutation({
    mutationFn: (filename: string) => api.restoreBackup(filename),
    onSuccess: (res) => {
      toast.success(res.notice)
      setRestoring(null)
      queryClient.invalidateQueries()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const deleteMutation = useMutation({
    mutationFn: (filename: string) => api.deleteBackup(filename),
    onSuccess: () => {
      toast.success('备份已删除')
      setDeleting(null)
      queryClient.invalidateQueries({ queryKey: ['backups'] })
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const download = async (rec: BackupRecord) => {
    try {
      await api.downloadBackup(rec.filename)
      toast.success('备份文件已开始下载')
    } catch (err) {
      toast.error((err as ApiError).message)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle>备份</CardTitle>
            <CardDescription>
              逻辑备份使用主密钥 AES-256-GCM 加密，含配置、用户与流量历史
            </CardDescription>
          </div>
          <Button
            variant="outline"
            onClick={() => createMutation.mutate()}
            disabled={createMutation.isPending}
          >
            {createMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <DatabaseBackup className="h-4 w-4" />
            )}
            立即备份
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {backupsQuery.isLoading ? (
          <TableSkeleton rows={3} />
        ) : backupsQuery.isError ? (
          <ErrorBox message={(backupsQuery.error as ApiError).message} />
        ) : (backupsQuery.data ?? []).length === 0 ? (
          <EmptyState
            icon={HardDriveDownload}
            title="还没有备份"
            description="点击「立即备份」生成第一个加密备份，或在设置里开启自动备份。"
          />
        ) : (
          <div className="space-y-2">
            {(backupsQuery.data ?? []).map((rec) => (
              <div
                key={rec.id}
                className="hud-slot flex flex-wrap items-center gap-3 px-3 py-2"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate font-mono text-sm">{rec.filename}</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {formatBytes(rec.sizeBytes)} · {rec.peerCount} 个客户端 · {rec.userCount} 个用户 ·
                    schema v{rec.schemaVersion} · {formatTime(rec.createdAt)}
                  </p>
                </div>
                <Badge variant={rec.kind === 'manual' ? 'default' : 'muted'}>
                  {rec.kind === 'manual' ? '手动' : rec.kind}
                </Badge>
                <div className="flex gap-1">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`下载 ${rec.filename}`}
                    onClick={() => download(rec)}
                  >
                    <Download className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`从 ${rec.filename} 恢复`}
                    onClick={() => setRestoring(rec)}
                  >
                    <Upload className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="text-destructive hover:bg-destructive/10"
                    aria-label={`删除 ${rec.filename}`}
                    onClick={() => setDeleting(rec)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      <ConfirmDialog
        open={restoring !== null}
        title="从备份恢复"
        message={`将把配置与账号回滚到 ${restoring?.filename} 的时刻，当前状态会被覆盖。该备份之后重置的密码也会一并回退，请确认已核对登录凭据。`}
        confirmText="确认恢复"
        submitting={restoreMutation.isPending}
        onConfirm={() => restoreMutation.mutate(restoring!.filename)}
        onClose={() => setRestoring(null)}
      />

      <ConfirmDialog
        open={deleting !== null}
        title="删除备份"
        message={`确认删除 ${deleting?.filename}？该操作不可恢复。`}
        confirmText="删除"
        submitting={deleteMutation.isPending}
        onConfirm={() => deleteMutation.mutate(deleting!.filename)}
        onClose={() => setDeleting(null)}
      />
    </Card>
  )
}

function ToggleRow({
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  label: string
  hint: string
  checked: boolean
  disabled?: boolean
  onChange: (next: boolean) => void
}) {
  return (
    <div className="hud-slot flex items-start justify-between gap-3 px-3 py-2">
      <div className="min-w-0">
        <p className="text-sm font-medium">{label}</p>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </div>
      <Select
        value={checked ? 'on' : 'off'}
        onChange={(e) => onChange(e.target.value === 'on')}
        disabled={disabled}
        className="w-24 shrink-0"
        aria-label={label}
      >
        <option value="on">开启</option>
        <option value="off">关闭</option>
      </Select>
    </div>
  )
}

function InfoRow({
  label,
  value,
  mono,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn('mt-0.5 break-words', mono && 'font-mono text-sm')}>{value}</dd>
    </div>
  )
}

function ErrorBox({ message }: { message: string }) {
  return (
    <div className="flex items-start gap-3 border border-destructive/50 bg-destructive/10 p-4 text-sm">
      <span className="hud-label shrink-0 pt-0.5 text-destructive">ERR</span>
      <div className="min-w-0">
        <p className="font-display text-xs font-semibold uppercase tracking-[0.14em] text-destructive">
          加载失败
        </p>
        <p className="mt-1 break-words text-muted-foreground">{message}</p>
      </div>
    </div>
  )
}