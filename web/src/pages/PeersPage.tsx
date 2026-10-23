import { useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Copy,
  Download,
  FileUp,
  Gauge,
  KeyRound,
  Link2,
  Loader2,
  Pencil,
  Plus,
  Search,
  Tags,
  Trash2,
  Upload,
  Users,
} from 'lucide-react'
import { ApiError, api, saveTextFile } from '@/lib/api'
import { roleCan } from '@/lib/permissions'
import { useVirtualList } from '@/lib/useVirtualList'
import { cn, copyText, formatBytes, formatRelative, firstMeaningful } from '@/lib/utils'
import { useAuth } from '@/components/providers/AuthProvider'
import { useToast } from '@/components/providers/ToastProvider'
import { Button } from '@/components/ui/Button'
import { Badge, StatusDot } from '@/components/ui/Badge'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/Card'
import { ConfirmDialog, Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select, Textarea } from '@/components/ui/Input'
import { EmptyState, TableSkeleton } from '@/components/ui/Skeleton'
import { SegmentedMeter } from '@/components/ui/StatTile'
import type { ImportResult, Peer, QuotaPeriod, SubscriptionLink } from '@/types/api'

const QUOTA_PERIODS: { value: QuotaPeriod; label: string }[] = [
  { value: 'never', label: '不限（永不重置）' },
  { value: 'daily', label: '每天重置' },
  { value: 'weekly', label: '每周重置' },
  { value: 'monthly', label: '每月重置' },
]

const CSV_TEMPLATE = [
  'name,address,allowedIps,endpoint,persistentKeepalive,note,trafficQuotaBytes,quotaPeriod,rateLimitMbps,enabled,expiresAt',
  'phone,10.8.0.10/32,0.0.0.0/0,,25,主力手机,107374182400,daily,20,0,true,2026-12-31T00:00:00Z',
  'laptop,,0.0.0.0/0,,25,,0,never,0,0,true,',
].join('\n')

type StatusFilter = 'all' | 'online' | 'offline' | 'disabled' | 'overQuota' | 'expiring'

/** 超过 50 个客户端时启用窗口化渲染，避免表格掉帧。 */
const VIRTUALIZE_THRESHOLD = 50
const ROW_HEIGHT = 56

export function PeersPage() {
  const { user } = useAuth()
  const toast = useToast()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [tagFilter, setTagFilter] = useState('')
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [editing, setEditing] = useState<Peer | null>(null)
  const [creating, setCreating] = useState(false)
  const [removing, setRemoving] = useState<Peer | null>(null)
  const [subscribing, setSubscribing] = useState<Peer | null>(null)
  const [importOpen, setImportOpen] = useState(false)

  const canCreate = roleCan(user?.role, 'peer:create')
  const canUpdate = roleCan(user?.role, 'peer:update')
  const canDelete = roleCan(user?.role, 'peer:delete')
  const canExport = roleCan(user?.role, 'config:export')
  const canImport = roleCan(user?.role, 'config:import')
  const canRevoke = roleCan(user?.role, 'peer:revoke')

  const peersQuery = useQuery({
    queryKey: ['peers'],
    queryFn: ({ signal }) => api.listPeers(signal),
    refetchInterval: 15_000,
  })

  const allPeers = peersQuery.data ?? []

  const allTags = useMemo(
    () => Array.from(new Set(allPeers.flatMap((p) => p.tags))).sort(),
    [allPeers],
  )

  const peers = useMemo(() => {
    const kw = keyword.trim().toLowerCase()
    return allPeers.filter((p) => {
      if (tagFilter && !p.tags.includes(tagFilter)) return false
      if (kw) {
        const hit =
          p.name.toLowerCase().includes(kw) ||
          p.address.includes(kw) ||
          p.tags.some((t) => t.toLowerCase().includes(kw))
        if (!hit) return false
      }
      switch (status) {
        case 'online':
          return p.stats?.online ?? false
        case 'offline':
          return !(p.stats?.online ?? false)
        case 'disabled':
          return !p.enabled
        case 'overQuota':
          return p.trafficQuotaBytes > 0 && p.quotaUsedBytes >= p.trafficQuotaBytes
        case 'expiring':
          return isExpiringSoon(p)
        default:
          return true
      }
    })
  }, [allPeers, keyword, status, tagFilter])

  const selectedIds = useMemo(() => Array.from(selected), [selected])

  const toggleSelect = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['peers'] })
    setSelected(new Set())
  }

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.deletePeer(id),
    onSuccess: () => {
      toast.success('客户端已删除')
      setRemoving(null)
      invalidate()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const revokeMutation = useMutation({
    mutationFn: (id: number) => api.rotatePeerKey(id),
    onSuccess: () => {
      toast.success('密钥已轮换，旧配置立即失效')
      invalidate()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const bulkRevokeMutation = useMutation({
    mutationFn: () => api.bulkRevoke(selectedIds),
    onSuccess: (res) => {
      toast.success(`已轮换 ${res.revoked} 个客户端的密钥`)
      invalidate()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const revokeAllMutation = useMutation({
    mutationFn: () => api.revokeAllPeers(),
    onSuccess: (res) => {
      toast.success(`已吊销全部 ${res.revoked} 个客户端，请重新下发配置`)
      setSelected(new Set())
      invalidate()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const bulkEnableMutation = useMutation({
    mutationFn: (enabled: boolean) => api.bulkEnable(selectedIds, enabled),
    onSuccess: (res) => {
      toast.success(`已${res.changed ? '更新' : '变更'} ${res.changed} 个客户端`)
      invalidate()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const configMutation = useMutation({
    // 不传 endpoint：交给后端按设置里的公网地址回退。
    // 显式传空串会让后端以为「用户明确要空地址」，
    // 于是导出的配置缺少 Endpoint 行，客户端永远连不上。
    mutationFn: (peer: Peer) => api.peerConfig(peer.id),
    onSuccess: async (conf) => {
      const ok = await copyText(conf)
      if (ok) toast.success('配置已复制到剪贴板')
      else toast.error('复制失败，请手动打开订阅或配置查看')
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const downloadCsv = async () => {
    try {
      await api.exportPeersCSV()
      toast.success('CSV 已开始下载')
    } catch (err) {
      toast.error((err as ApiError).message)
    }
  }

  const hasBulk = selectedIds.length > 0

  return (
    <div className="space-y-5">
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// NODES-03</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              客户端
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">
              共 <span className="font-mono text-foreground">{allPeers.length}</span> 个，在线{' '}
              <span className="font-mono text-success">
                {allPeers.filter((p) => p.stats?.online).length}
              </span>{' '}
              个
              {peers.length !== allPeers.length && ` · 当前筛选 ${peers.length} 个`}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {canImport && (
              <Button variant="outline" onClick={() => setImportOpen(true)}>
                <FileUp className="h-4 w-4" />
                导入 CSV
              </Button>
            )}
            {canExport && (
              <Button variant="outline" onClick={downloadCsv}>
                <Download className="h-4 w-4" />
                导出 CSV
              </Button>
            )}
            {canCreate && (
              <Button onClick={() => setCreating(true)}>
                <Plus className="h-4 w-4" />
                新建客户端
              </Button>
            )}
          </div>
        </div>
        <div
          className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80"
          aria-hidden="true"
        />
      </header>

      <Card>
        <CardHeader className="gap-3">
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
            <div className="relative lg:col-span-2">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                placeholder="按名称、地址或标签搜索"
                className="pl-9"
                aria-label="搜索客户端"
              />
            </div>
            <Select
              value={status}
              onChange={(e) => setStatus(e.target.value as StatusFilter)}
              aria-label="按状态筛选"
            >
              <option value="all">全部状态</option>
              <option value="online">在线</option>
              <option value="offline">离线</option>
              <option value="disabled">已停用</option>
              <option value="overQuota">配额用尽</option>
              <option value="expiring">即将到期</option>
            </Select>
            <Select
              value={tagFilter}
              onChange={(e) => setTagFilter(e.target.value)}
              aria-label="按标签筛选"
            >
              <option value="">全部标签</option>
              {allTags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag}
                </option>
              ))}
            </Select>
          </div>
          <CardDescription>
            名称、标签与备注用于区分用途；配额超限或到期的客户端会被策略自动停用
          </CardDescription>
        </CardHeader>

        {hasBulk && (canUpdate || canRevoke) && (
          <div className="flex flex-wrap items-center gap-2 border-y border-primary/30 bg-primary/5 px-4 py-2.5">
            <span className="font-display text-xs font-semibold uppercase tracking-[0.12em] text-primary">
              已选中 <span className="font-mono tabular-nums">{selectedIds.length}</span> 个
            </span>
            {canUpdate && (
              <Button
                size="sm"
                variant="outline"
                disabled={bulkEnableMutation.isPending}
                onClick={() => bulkEnableMutation.mutate(true)}
              >
                批量启用
              </Button>
            )}
            {canUpdate && (
              <Button
                size="sm"
                variant="outline"
                disabled={bulkEnableMutation.isPending}
                onClick={() => bulkEnableMutation.mutate(false)}
              >
                批量停用
              </Button>
            )}
            <BulkTagButton ids={selectedIds} onDone={invalidate} />
            {canRevoke && (
              <Button
                size="sm"
                variant="outline"
                className="text-destructive"
                disabled={bulkRevokeMutation.isPending || revokeAllMutation.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      `确认轮换 ${selectedIds.length} 个客户端的密钥？所有旧配置将立即失效。`,
                    )
                  ) {
                    bulkRevokeMutation.mutate()
                  }
                }}
              >
                批量吊销
              </Button>
            )}
            {/* 全量吊销单独一个入口，不复用「全选」：
                密钥泄露应急时要处理的是「库里全部 Peer」，
                而勾选态受虚拟滚动与搜索影响，漏掉几台不会有任何提示。
                两者混成一个按钮会让应急操作变成一次不可验证的点击。 */}
            {canRevoke && (
              <Button
                size="sm"
                variant="outline"
                className="text-destructive"
                disabled={revokeAllMutation.isPending || bulkRevokeMutation.isPending}
                onClick={() => {
                  // 用 allPeers 而不是当前筛选后的 peers：
                  // 确认框里报的数必须等于服务端将要遍历的数，
                  // 否则会出现「说吊销 3 个、实际吊销 50 个」。
                  if (
                    window.confirm(
                      `确认吊销全部 ${allPeers.length} 个客户端的密钥？\n` +
                        '这是密钥泄露时的应急操作：所有客户端都需要重新下发配置。',
                    )
                  ) {
                    revokeAllMutation.mutate()
                  }
                }}
              >
                全量吊销
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="ml-auto"
              onClick={() => setSelected(new Set())}
            >
              取消选择
            </Button>
          </div>
        )}

        <CardContent>
          {peersQuery.isLoading ? (
            <TableSkeleton rows={6} />
          ) : peersQuery.isError ? (
            <ErrorBox message={(peersQuery.error as ApiError).message} />
          ) : peers.length === 0 ? (
            <EmptyState
              icon={Users}
              title={
                allPeers.length === 0
                  ? '还没有客户端'
                  : '没有符合条件的客户端'
              }
              description={
                allPeers.length === 0
                  ? '创建客户端后可导出配置，或用 CSV 批量导入。'
                  : '调整筛选条件后再试一次。'
              }
              action={
                canCreate && allPeers.length === 0 ? (
                  <Button onClick={() => setCreating(true)}>
                    <Plus className="h-4 w-4" />
                    新建客户端
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <PeerList
              peers={peers}
              selected={selected}
              onToggle={toggleSelect}
              canUpdate={canUpdate}
              canExport={canExport}
              canRevoke={canRevoke}
              canDelete={canDelete}
              configPending={configMutation.isPending}
              onCopyConfig={(p) => configMutation.mutate(p)}
              onEdit={setEditing}
              onSubscribe={setSubscribing}
              onRevoke={(id) => revokeMutation.mutate(id)}
              onDelete={setRemoving}
            />
          )}
        </CardContent>
      </Card>

      <PeerFormDialog
        open={creating || editing !== null}
        peer={editing}
        onClose={() => {
          setCreating(false)
          setEditing(null)
        }}
        onSaved={() => {
          setCreating(false)
          setEditing(null)
          invalidate()
        }}
      />

      <ImportDialog open={importOpen} onClose={() => setImportOpen(false)} onDone={invalidate} />

      <SubscriptionDialog peer={subscribing} onClose={() => setSubscribing(null)} />

      <ConfirmDialog
        open={removing !== null}
        title="删除客户端"
        message={`确认删除「${removing?.name}」？该客户端的密钥、配置与订阅链接会一并失效，操作不可撤销。`}
        confirmText="删除"
        onConfirm={() => deleteMutation.mutate(removing!.id)}
        onClose={() => setRemoving(null)}
      />
    </div>
  )
}

function PeerList({
  peers,
  selected,
  onToggle,
  canUpdate,
  canExport,
  canRevoke,
  canDelete,
  configPending,
  onCopyConfig,
  onEdit,
  onSubscribe,
  onRevoke,
  onDelete,
}: {
  peers: Peer[]
  selected: Set<number>
  onToggle: (id: number) => void
  canUpdate: boolean
  canExport: boolean
  canRevoke: boolean
  canDelete: boolean
  configPending: boolean
  onCopyConfig: (peer: Peer) => void
  onEdit: (peer: Peer) => void
  onSubscribe: (peer: Peer) => void
  onRevoke: (id: number) => void
  onDelete: (peer: Peer) => void
}) {
  // 客户端数量大时只渲染视口内的行，否则 DOM 节点数会随规模线性膨胀。
  const virtual = useVirtualList(peers, ROW_HEIGHT)
  const virtualized = peers.length >= VIRTUALIZE_THRESHOLD

  if (virtualized) {
    return (
      <div
        ref={virtual.containerRef}
        className="hud-slot max-h-[70vh] overflow-y-auto"
        role="list"
        aria-label="客户端列表"
      >
        <div style={{ height: virtual.paddingTop }} aria-hidden="true" />
        {virtual.visible.map((peer) => (
          <PeerRow
            key={peer.id}
            peer={peer}
            height={ROW_HEIGHT}
            selected={selected.has(peer.id)}
            canUpdate={canUpdate}
            canExport={canExport}
            canRevoke={canRevoke}
            canDelete={canDelete}
            configPending={configPending}
            onToggle={onToggle}
            onCopyConfig={onCopyConfig}
            onEdit={onEdit}
            onSubscribe={onSubscribe}
            onRevoke={onRevoke}
            onDelete={onDelete}
          />
        ))}
        <div style={{ height: virtual.paddingBottom }} aria-hidden="true" />
      </div>
    )
  }

  return (
    <div className="space-y-2">
      {peers.map((peer) => (
        <PeerRow
          key={peer.id}
          peer={peer}
          selected={selected.has(peer.id)}
          canUpdate={canUpdate}
          canExport={canExport}
          canRevoke={canRevoke}
          canDelete={canDelete}
          configPending={configPending}
          onToggle={onToggle}
          onCopyConfig={onCopyConfig}
          onEdit={onEdit}
          onSubscribe={onSubscribe}
          onRevoke={onRevoke}
          onDelete={onDelete}
        />
      ))}
    </div>
  )
}

function PeerRow({
  peer,
  selected,
  height,
  canUpdate,
  canExport,
  canRevoke,
  canDelete,
  configPending,
  onToggle,
  onCopyConfig,
  onEdit,
  onSubscribe,
  onRevoke,
  onDelete,
}: {
  peer: Peer
  selected: boolean
  height?: number
  canUpdate: boolean
  canExport: boolean
  canRevoke: boolean
  canDelete: boolean
  configPending: boolean
  onToggle: (id: number) => void
  onCopyConfig: (peer: Peer) => void
  onEdit: (peer: Peer) => void
  onSubscribe: (peer: Peer) => void
  onRevoke: (id: number) => void
  onDelete: (peer: Peer) => void
}) {
  const quotaPercent =
    peer.trafficQuotaBytes > 0
      ? (peer.quotaUsedBytes / peer.trafficQuotaBytes) * 100
      : 0

  return (
    <div
      className={cn(
        'flex items-center gap-3 border px-3 py-2 transition-colors',
        selected
          ? 'border-primary/60 bg-primary/10 shadow-neon-sm'
          : 'border-border/60 bg-background/40 hover:border-primary/40 hover:bg-primary/5',
      )}
      style={height ? { height, minHeight: height } : undefined}
      role="listitem"
    >
      <input
        type="checkbox"
        className="h-4 w-4 shrink-0 accent-primary"
        checked={selected}
        onChange={() => onToggle(peer.id)}
        aria-label={`选择 ${peer.name}`}
      />

      <StatusDot online={peer.stats?.online ?? false} />

      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <p className="truncate text-sm font-medium">{peer.name}</p>
          <StateBadges peer={peer} />
          {peer.tags.map((tag) => (
            <Badge key={tag} variant="muted">
              {tag}
            </Badge>
          ))}
        </div>
        <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
          {peer.address}
          {peer.note ? ` · ${peer.note}` : ''}
        </p>
      </div>

      <div className="hidden w-44 shrink-0 sm:block">
        {peer.trafficQuotaBytes > 0 ? (
          <>
            <div className="flex justify-between font-mono text-xs">
              <span className="text-muted-foreground">配额</span>
              <span className={cn(quotaPercent >= 100 && 'text-destructive')}>
                {formatBytes(peer.quotaUsedBytes)} / {formatBytes(peer.trafficQuotaBytes)}
              </span>
            </div>
            <div className="mt-1.5">
              <SegmentedMeter
                percent={quotaPercent}
                segments={12}
                barClass={
                  quotaPercent >= 100
                    ? 'bg-destructive shadow-[0_0_6px_hsl(var(--destructive)/0.8)]'
                    : 'bg-primary shadow-[0_0_6px_hsl(var(--primary)/0.7)]'
                }
              />
            </div>
          </>
        ) : (
          <p className="font-mono text-xs text-muted-foreground">不限流量</p>
        )}
      </div>

      <div className="hidden w-24 shrink-0 text-right md:block">
        <p className="font-mono text-xs tabular-nums">{formatBytes(peer.stats?.rxBytes ?? 0)}</p>
        <p className="font-mono text-xs tabular-nums text-muted-foreground">
          ↑ {formatBytes(peer.stats?.txBytes ?? 0)}
        </p>
      </div>

      <p className="hidden w-20 shrink-0 text-xs text-muted-foreground lg:block">
        {formatRelative(firstMeaningful(peer.stats?.lastHandshake, peer.lastSeenAt))}
      </p>

      <div className="flex shrink-0 gap-1">
        {canExport && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`复制 ${peer.name} 配置`}
            disabled={configPending}
            onClick={() => onCopyConfig(peer)}
          >
            <Copy className="h-4 w-4" />
          </Button>
        )}
        {canExport && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`${peer.name} 订阅链接`}
            onClick={() => onSubscribe(peer)}
          >
            <Link2 className="h-4 w-4" />
          </Button>
        )}
        {canRevoke && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`轮换 ${peer.name} 密钥`}
            onClick={() => {
              if (window.confirm(`确认轮换「${peer.name}」的密钥？旧配置将立即失效。`)) {
                onRevoke(peer.id)
              }
            }}
          >
            <KeyRound className="h-4 w-4" />
          </Button>
        )}
        {canUpdate && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`编辑 ${peer.name}`}
            onClick={() => onEdit(peer)}
          >
            <Pencil className="h-4 w-4" />
          </Button>
        )}
        {canDelete && (
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-destructive hover:bg-destructive/10"
            aria-label={`删除 ${peer.name}`}
            onClick={() => onDelete(peer)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        )}
      </div>
    </div>
  )
}

function StateBadges({ peer }: { peer: Peer }) {
  if (!peer.enabled) {
    return (
      <Badge variant="muted">
        {peer.suspendedBy === 'quota'
          ? '配额停用'
          : peer.suspendedBy === 'expired'
            ? '到期停用'
            : '已停用'}
      </Badge>
    )
  }
  if (peer.expiresAt && new Date(peer.expiresAt).getTime() - Date.now() < 3 * 86400_000) {
    return <Badge variant="warning">即将到期</Badge>
  }
  if (peer.rateLimitMbps > 0) {
    return (
      <Badge variant="outline" className="font-mono">
        {peer.rateLimitMbps} Mbps
      </Badge>
    )
  }
  return null
}

function BulkTagButton({ ids, onDone }: { ids: number[]; onDone: () => void }) {
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [tag, setTag] = useState('')
  const [remove, setRemove] = useState(false)

  const mutation = useMutation({
    mutationFn: () => api.bulkTag(ids, tag.trim(), remove),
    onSuccess: (res) => {
      toast.success(`已${remove ? '移除' : '添加'}标签，${res.changed} 个客户端生效`)
      setOpen(false)
      setTag('')
      onDone()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  return (
    <>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        <Tags className="h-3.5 w-3.5" />
        批量标签
      </Button>
      <Dialog
        open={open}
        title="批量设置标签"
        description={`将作用于选中的 ${ids.length} 个客户端`}
        onClose={() => setOpen(false)}
        className="sm:max-w-sm"
        footer={
          <>
            <Button variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button
              onClick={() => mutation.mutate()}
              disabled={mutation.isPending || tag.trim() === ''}
            >
              {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              应用
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field label="标签" htmlFor="bulk-tag" hint="客户端已拥有该标签时会被跳过">
            <Input
              id="bulk-tag"
              value={tag}
              onChange={(e) => setTag(e.target.value)}
              placeholder="work"
            />
          </Field>
          <Select
            value={remove ? 'remove' : 'add'}
            onChange={(e) => setRemove(e.target.value === 'remove')}
            aria-label="操作类型"
          >
            <option value="add">添加标签</option>
            <option value="remove">移除标签</option>
          </Select>
        </div>
      </Dialog>
    </>
  )
}

function ImportDialog({
  open,
  onClose,
  onDone,
}: {
  open: boolean
  onClose: () => void
  onDone: () => void
}) {
  const toast = useToast()
  const [csv, setCsv] = useState('')
  const [result, setResult] = useState<ImportResult | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const importMutation = useMutation({
    mutationFn: () => api.importPeersCSV(csv),
    onSuccess: (res) => {
      setResult(res)
      if (res.failed === 0) toast.success(`导入完成，新建 ${res.created} 个客户端`)
      else toast.error(`导入完成，但有 ${res.failed} 行失败`)
      onDone()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const close = () => {
    setCsv('')
    setResult(null)
    onClose()
  }

  return (
    <Dialog
      open={open}
      title="从 CSV 导入客户端"
      description="逐行校验，单行失败不影响其他行；结果会列出具体原因"
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            关闭
          </Button>
          <Button
            onClick={() => importMutation.mutate()}
            disabled={importMutation.isPending || csv.trim() === ''}
          >
            {importMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Upload className="h-4 w-4" />
            )}
            开始导入
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => fileRef.current?.click()}
          >
            <Upload className="h-3.5 w-3.5" />
            选择文件
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => saveTextFile('wgpanel-peers-template.csv', CSV_TEMPLATE)}
          >
            <Download className="h-3.5 w-3.5" />
            下载模板
          </Button>
          <input
            ref={fileRef}
            type="file"
            accept=".csv,text/csv,text/plain"
            className="hidden"
            onChange={async (e) => {
              const file = e.target.files?.[0]
              if (!file) return
              setCsv(await file.text())
              e.target.value = ''
            }}
          />
        </div>

        <Field label="CSV 内容" htmlFor="csv-body" hint="表头字段与导出保持一致，多标签用 | 分隔">
          <Textarea
            id="csv-body"
            value={csv}
            onChange={(e) => setCsv(e.target.value)}
            rows={8}
            className="font-mono text-xs"
            placeholder={CSV_TEMPLATE}
          />
        </Field>

        {result && (
          <div className="hud-slot space-y-2 p-3 text-sm">
            <div className="flex flex-wrap gap-2">
              <Badge variant="success">新建 {result.created}</Badge>
              <Badge variant="muted">跳过 {result.skipped}</Badge>
              <Badge variant={result.failed > 0 ? 'destructive' : 'muted'}>
                失败 {result.failed}
              </Badge>
            </div>
            {result.errors.length > 0 && (
              <ul className="max-h-40 space-y-1 overflow-y-auto text-xs">
                {result.errors.map((err, i) => (
                  <li key={`${err.line}-${i}`} className="flex gap-2">
                    <span className="shrink-0 font-mono text-muted-foreground">
                      第 {err.line} 行
                    </span>
                    <span className="min-w-0 break-words">
                      {err.name ? `${err.name}：` : ''}
                      {err.reason}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </div>
    </Dialog>
  )
}

function SubscriptionDialog({
  peer,
  onClose,
}: {
  peer: Peer | null
  onClose: () => void
}) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const [link, setLink] = useState<SubscriptionLink | null>(null)
  const [lastPeerId, setLastPeerId] = useState<number | null>(null)

  if ((peer?.id ?? null) !== lastPeerId) {
    setLastPeerId(peer?.id ?? null)
    setLink(null)
  }

  const issueMutation = useMutation({
    mutationFn: (id: number) => api.issueSubscription(id),
    onSuccess: (res) => {
      setLink(res)
      toast.success('订阅链接已签发')
      queryClient.invalidateQueries({ queryKey: ['peers'] })
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const revokeMutation = useMutation({
    mutationFn: (id: number) => api.revokeSubscription(id),
    onSuccess: () => {
      setLink(null)
      toast.success('订阅链接已撤销')
      queryClient.invalidateQueries({ queryKey: ['peers'] })
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const copy = async () => {
    if (!link) return
    const ok = await copyText(link.url)
    if (ok) toast.success('订阅链接已复制')
    else toast.error('复制失败，请手动选中链接')
  }

  const busy = issueMutation.isPending || revokeMutation.isPending

  return (
    <Dialog
      open={peer !== null}
      title={`${peer?.name ?? ''} 的订阅链接`}
      description="客户端可通过该链接自动更新配置；撤销后旧链接立即失效"
      onClose={onClose}
      className="sm:max-w-xl"
      footer={
        <>
          {link && (
            <Button
              variant="outline"
              className="text-destructive"
              disabled={busy}
              onClick={() => revokeMutation.mutate(peer!.id)}
            >
              撤销链接
            </Button>
          )}
          <Button variant="outline" onClick={copy} disabled={!link}>
            <Copy className="h-4 w-4" />
            复制
          </Button>
          <Button onClick={() => issueMutation.mutate(peer!.id)} disabled={busy}>
            {issueMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Link2 className="h-4 w-4" />
            )}
            {link ? '重新签发' : '签发订阅'}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {link ? (
          <div className="space-y-2">
            <code className="hud-slot block break-all p-3 font-mono text-xs text-neon-cyan">
              {link.url}
            </code>
            <p className="text-xs text-muted-foreground">
              签发于 {new Date(link.issuedAt).toLocaleString()} · 令牌
              <span className="font-mono"> {link.token.slice(0, 8)}…</span>
            </p>
            <p className="text-xs text-muted-foreground">
              可直接填入 v2rayNG、Shadowrocket 等客户端的订阅地址。
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            尚未签发订阅链接。签发后令牌本身即凭据，任何拿到链接的人都能获取该客户端的配置。
          </p>
        )}
      </div>
    </Dialog>
  )
}

function PeerFormDialog({
  open,
  peer,
  onClose,
  onSaved,
}: {
  open: boolean
  peer: Peer | null
  onClose: () => void
  onSaved: () => void
}) {
  const toast = useToast()

  const [name, setName] = useState(peer?.name ?? '')
  const [address, setAddress] = useState(peer?.address ?? '')
  const [allowedIps, setAllowedIps] = useState(peer?.allowedIps ?? '0.0.0.0/0, ::/0')
  const [keepalive, setKeepalive] = useState(peer?.persistentKeepalive ?? 25)
  const [enabled, setEnabled] = useState(peer?.enabled ?? true)
  const [tags, setTags] = useState((peer?.tags ?? []).join(', '))
  const [note, setNote] = useState(peer?.note ?? '')
  const [quotaGB, setQuotaGB] = useState(
    peer ? Math.round((peer.trafficQuotaBytes / 1024 ** 3) * 100) / 100 : 0,
  )
  const [quotaPeriod, setQuotaPeriod] = useState<QuotaPeriod>(peer?.quotaPeriod ?? 'never')
  const [rateLimit, setRateLimit] = useState(peer?.rateLimitMbps ?? 0)
  const [expiresAt, setExpiresAt] = useState(
    peer?.expiresAt ? toDateTimeLocal(peer.expiresAt) : '',
  )

  const [lastPeerId, setLastPeerId] = useState<number | null>(null)
  if (open && (peer?.id ?? null) !== lastPeerId) {
    setLastPeerId(peer?.id ?? null)
    setName(peer?.name ?? '')
    setAddress(peer?.address ?? '')
    setAllowedIps(peer?.allowedIps ?? '0.0.0.0/0, ::/0')
    setKeepalive(peer?.persistentKeepalive ?? 25)
    setEnabled(peer?.enabled ?? true)
    setTags((peer?.tags ?? []).join(', '))
    setNote(peer?.note ?? '')
    setQuotaGB(peer ? Math.round((peer.trafficQuotaBytes / 1024 ** 3) * 100) / 100 : 0)
    setQuotaPeriod(peer?.quotaPeriod ?? 'never')
    setRateLimit(peer?.rateLimitMbps ?? 0)
    setExpiresAt(peer?.expiresAt ? toDateTimeLocal(peer.expiresAt) : '')
  }

  const saveMutation = useMutation({
    mutationFn: async () => {
      const payload: Partial<Peer> = {
        name: name.trim(),
        allowedIps,
        persistentKeepalive: keepalive,
        enabled,
        tags: tags
          .split(',')
          .map((t) => t.trim())
          .filter(Boolean),
        note,
        trafficQuotaBytes: Math.round(quotaGB * 1024 ** 3),
        quotaPeriod,
        rateLimitMbps: rateLimit,
        expiresAt: expiresAt ? new Date(expiresAt).toISOString() : '',
      }
      if (peer) {
        if (address !== peer.address) payload.address = address
        return api.updatePeer(peer.id, payload)
      }
      return api.createPeer({ ...payload, address: address || undefined })
    },
    onSuccess: () => {
      toast.success(peer ? '客户端已更新' : '客户端已创建')
      onSaved()
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  return (
    <Dialog
      open={open}
      title={peer ? '编辑客户端' : '新建客户端'}
      description={
        peer ? '修改后立即生效，客户端无需重连。' : '地址留空时自动从服务端网段分配。'
      }
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saveMutation.isPending}>
            取消
          </Button>
          <Button
            onClick={() => saveMutation.mutate()}
            disabled={saveMutation.isPending || name.trim() === ''}
          >
            {saveMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            保存
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (name.trim() !== '') saveMutation.mutate()
        }}
      >
        <Field label="名称" htmlFor="peer-name" hint="建议使用设备名，如 laptop / iphone">
          <Input
            id="peer-name"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="laptop"
          />
        </Field>

        <Field
          label="隧道地址"
          htmlFor="peer-address"
          hint={peer ? '修改后客户端需重新导入配置' : '留空自动分配，例如 10.8.0.2/32'}
        >
          <Input
            id="peer-address"
            value={address}
            onChange={(e) => setAddress(e.target.value)}
            placeholder="自动分配"
            className="font-mono"
          />
        </Field>

        <Field
          label="AllowedIPs"
          htmlFor="peer-allowed"
          hint="客户端侧：决定哪些流量走隧道。只写入客户端配置，不会下发到服务端"
        >
          <Input
            id="peer-allowed"
            value={allowedIps}
            onChange={(e) => setAllowedIps(e.target.value)}
            className="font-mono"
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Keepalive（秒）" htmlFor="peer-keepalive" hint="0 表示关闭">
            <Input
              id="peer-keepalive"
              type="number"
              min={0}
              max={65535}
              value={keepalive}
              onChange={(e) => setKeepalive(Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="标签" htmlFor="peer-tags" hint="逗号分隔">
            <Input
              id="peer-tags"
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="work, family"
            />
          </Field>
        </div>

        <Field label="备注" htmlFor="peer-note">
          <Input
            id="peer-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="选填"
          />
        </Field>

        <div className="hud-slot space-y-3 p-3">
          <p className="flex items-center gap-2 text-sm font-medium">
            <Gauge className="h-4 w-4 text-primary" />
            配额与限速
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="流量配额（GB）" htmlFor="peer-quota" hint="0 表示不限">
              <Input
                id="peer-quota"
                type="number"
                min={0}
                step={0.5}
                value={quotaGB}
                onChange={(e) => setQuotaGB(Number(e.target.value))}
                className="font-mono"
              />
            </Field>
            <Field label="配额周期" htmlFor="peer-period" hint="周期切换后用量清零">
              <Select
                id="peer-period"
                value={quotaPeriod}
                onChange={(e) => setQuotaPeriod(e.target.value as QuotaPeriod)}
              >
                {QUOTA_PERIODS.map((p) => (
                  <option key={p.value} value={p.value}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="带宽上限（Mbps）" htmlFor="peer-rate" hint="0 表示不限，按 tc 限速">
              <Input
                id="peer-rate"
                type="number"
                min={0}
                max={100000}
                value={rateLimit}
                onChange={(e) => setRateLimit(Number(e.target.value))}
                className="font-mono"
              />
            </Field>
          </div>
          {peer && peer.quotaUsedBytes > 0 && (
            <p className="text-xs text-muted-foreground">
              当前周期已用 {formatBytes(peer.quotaUsedBytes)}
              {peer.trafficQuotaBytes > 0 &&
                ` / ${formatBytes(peer.trafficQuotaBytes)}`}
            </p>
          )}
        </div>

        <Field label="到期时间" htmlFor="peer-expires" hint="留空表示永久有效">
          <Input
            id="peer-expires"
            type="datetime-local"
            value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)}
          />
        </Field>

        <div className="hud-slot flex items-center justify-between px-3 py-2">
          <div>
            <p className="text-sm font-medium">启用</p>
            <p className="text-xs text-muted-foreground">
              停用后立即断开且不删除配置；由策略停用的客户端不受此开关影响
            </p>
          </div>
          <Select
            value={enabled ? 'on' : 'off'}
            onChange={(e) => setEnabled(e.target.value === 'on')}
            className="w-24"
            aria-label="是否启用"
          >
            <option value="on">启用</option>
            <option value="off">停用</option>
          </Select>
        </div>
      </form>
    </Dialog>
  )
}

function isExpiringSoon(peer: Peer): boolean {
  if (!peer.expiresAt) return false
  const remain = new Date(peer.expiresAt).getTime() - Date.now()
  return remain > 0 && remain < 3 * 86400_000
}

/** ISO 时间转 <input type="datetime-local"> 需要的本地时间格式。 */
function toDateTimeLocal(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
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