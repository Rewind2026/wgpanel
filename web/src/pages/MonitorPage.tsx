import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  Clock,
  Cpu,
  HardDrive,
  MemoryStick,
  Radio,
  Search,
  Signal,
  Users,
} from 'lucide-react'
import { ApiError, api } from '@/lib/api'
import { useMetricsStream } from '@/lib/useMetricsStream'
import { cn, formatBytes, formatDuration, formatRate, formatRelative, firstMeaningful } from '@/lib/utils'
import { Badge, StatusDot } from '@/components/ui/Badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/Card'
import { Input, Select } from '@/components/ui/Input'
import { TrafficChart } from '@/components/charts/TrafficChart'
import { EmptyState, TableSkeleton } from '@/components/ui/Skeleton'
import { StatTile, SegmentedMeter } from '@/components/ui/StatTile'
import type { Peer, PeerStats, TrafficRange } from '@/types/api'

const RANGES: { value: TrafficRange; label: string; bucket: string }[] = [
  { value: '1h', label: '最近 1 小时', bucket: '原始采样' },
  { value: '24h', label: '最近 24 小时', bucket: '10 分钟粒度' },
  { value: '7d', label: '最近 7 天', bucket: '小时粒度' },
  { value: '30d', label: '最近 30 天', bucket: '天粒度' },
]

type SortKey = 'name' | 'rx' | 'tx'

export function MonitorPage() {
  const { snapshot, connected } = useMetricsStream(true)
  const [range, setRange] = useState<TrafficRange>('24h')
  const [peerId, setPeerId] = useState<number | ''>('')
  const [keyword, setKeyword] = useState('')
  const [sort, setSort] = useState<SortKey>('rx')
  const [onlyOnline, setOnlyOnline] = useState(false)

  const peersQuery = useQuery({
    queryKey: ['peers'],
    queryFn: ({ signal }) => api.listPeers(signal),
    refetchInterval: 30_000,
  })

  const trafficQuery = useQuery({
    queryKey: ['traffic', range, peerId],
    queryFn: ({ signal }) => api.traffic(range, peerId === '' ? undefined : peerId, signal),
    refetchInterval: range === '1h' ? 30_000 : 300_000,
  })

  const peers = peersQuery.data ?? []
  const nameById = useMemo(() => new Map(peers.map((p) => [p.id, p.name])), [peers])

  // 列表页 15 秒轮询带回来的统计比 SSE 帧略滞后，
  // 合并两者：SSE 帧优先，缺失的补上列表数据，避免整张表闪成空值。
  const rows = useMemo(() => mergeStats(peers, snapshot?.peers), [peers, snapshot?.peers])

  const filtered = useMemo(() => {
    const kw = keyword.trim().toLowerCase()
    const list = rows.filter((row) => {
      if (onlyOnline && !row.stats.online) return false
      if (kw === '') return true
      return row.peer.name.toLowerCase().includes(kw) || row.peer.address.includes(kw)
    })
    return list.sort((a, b) => {
      if (sort === 'name') return a.peer.name.localeCompare(b.peer.name)
      if (sort === 'tx') return b.stats.txBytes - a.stats.txBytes
      return b.stats.rxBytes - a.stats.rxBytes
    })
  }, [rows, keyword, onlyOnline, sort])

  const rangeMeta = RANGES.find((r) => r.value === range)!
  const series = trafficQuery.data

  return (
    <div className="space-y-5">
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// LIVE-02</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              实时监控
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">
              实时数据经 SSE 推送，历史曲线来自本地聚合表（原始采样 / 10 分钟 / 小时 / 天）
            </p>
          </div>
          <Badge variant={connected ? 'success' : 'destructive'}>
            <StatusDot online={connected} />
            {connected ? '实时推送已连接' : '推送断开，重连中'}
          </Badge>
        </div>
        <div
          className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80"
          aria-hidden="true"
        />
      </header>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile
          icon={Users}
          label="在线客户端"
          value={
            snapshot
              ? `${snapshot.tunnel.onlinePeers} / ${snapshot.tunnel.totalPeers}`
              : '—'
          }
          hint={snapshot?.tunnel.interfaceUp ? '隧道接口正常' : '隧道接口未就绪'}
          accent={snapshot?.tunnel.interfaceUp ? 'success' : 'warning'}
        />
        <StatTile
          icon={ArrowDownToLine}
          label="下行速率"
          value={formatRate(snapshot?.tunnel.rxRateBps ?? 0)}
          hint={`累计 ${formatBytes(snapshot?.tunnel.rxBytes ?? 0)}`}
        />
        <StatTile
          icon={ArrowUpFromLine}
          label="上行速率"
          value={formatRate(snapshot?.tunnel.txRateBps ?? 0)}
          hint={`累计 ${formatBytes(snapshot?.tunnel.txBytes ?? 0)}`}
        />
        <StatTile
          icon={Radio}
          label="公网出口"
          value={snapshot?.tunnel.publicEndpoint || '—'}
          hint={
            snapshot
              ? `${snapshot.tunnel.interface}:${snapshot.tunnel.listenPort}`
              : '—'
          }
          accent="primary"
        />
      </div>

      <div className="grid gap-5 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader className="gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <CardTitle>历史流量</CardTitle>
                <CardDescription>
                  {rangeMeta.label} · {rangeMeta.bucket}
                  {series ? ` · ${series.points.length} 个采样点` : ''}
                </CardDescription>
              </div>
              <div className="flex flex-wrap gap-2">
                <Select
                  value={range}
                  onChange={(e) => setRange(e.target.value as TrafficRange)}
                  className="h-8 w-32 text-xs"
                  aria-label="时间范围"
                >
                  {RANGES.map((r) => (
                    <option key={r.value} value={r.value}>
                      {r.label}
                    </option>
                  ))}
                </Select>
                <Select
                  value={String(peerId)}
                  onChange={(e) =>
                    setPeerId(e.target.value === '' ? '' : Number(e.target.value))
                  }
                  className="h-8 w-40 text-xs"
                  aria-label="筛选客户端"
                >
                  <option value="">全部客户端</option>
                  {peers.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              </div>
            </div>
            {peerId !== '' && (
              <p className="text-xs text-muted-foreground">
                当前只显示「{nameById.get(peerId) ?? `#${peerId}`}」的流量
              </p>
            )}
          </CardHeader>
          <CardContent>
            {trafficQuery.isLoading ? (
              <TableSkeleton rows={4} />
            ) : trafficQuery.isError ? (
              <ErrorBox message={(trafficQuery.error as ApiError).message} />
            ) : series ? (
              <TrafficChart points={series.points} fromTs={series.from} toTs={series.to} />
            ) : null}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>资源占用</CardTitle>
            <CardDescription>宿主机与容器指标</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Gauge
              icon={Cpu}
              label="CPU"
              percent={snapshot?.system.cpuPercent ?? 0}
              detail={`${(snapshot?.system.cpuPercent ?? 0).toFixed(1)}%`}
            />
            <Gauge
              icon={MemoryStick}
              label="内存"
              percent={snapshot?.system.memPercent ?? 0}
              detail={`${formatBytes(snapshot?.system.memUsedBytes ?? 0)} · ${(snapshot?.system.memPercent ?? 0).toFixed(1)}%`}
            />
            <Gauge
              icon={HardDrive}
              label="磁盘"
              percent={snapshot?.system.diskPercent ?? 0}
              detail={`${formatBytes(snapshot?.system.diskUsedBytes ?? 0)} · ${(snapshot?.system.diskPercent ?? 0).toFixed(1)}%`}
            />
            <div className="space-y-2 border-t border-border/60 pt-3 text-xs">
              <Row label="网卡下行" value={formatRate(snapshot?.system.netRxBps ?? 0)} cyan={false} />
              <Row label="网卡上行" value={formatRate(snapshot?.system.netTxBps ?? 0)} cyan />
              <Row label="协程数" value={String(snapshot?.system.goroutines ?? 0)} />
              <Row label="运行时长" value={formatDuration(snapshot?.system.uptimeSeconds ?? 0)} />
            </div>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="gap-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <CardTitle>客户端实时状态</CardTitle>
            <div className="flex flex-wrap items-center gap-2">
              <div className="relative">
                <Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={keyword}
                  onChange={(e) => setKeyword(e.target.value)}
                  placeholder="搜索名称或 IP"
                  className="h-8 w-40 pl-8 text-xs"
                  aria-label="搜索客户端"
                />
              </div>
              <Select
                value={sort}
                onChange={(e) => setSort(e.target.value as SortKey)}
                className="h-8 w-28 text-xs"
                aria-label="排序方式"
              >
                <option value="rx">按下行排序</option>
                <option value="tx">按上行排序</option>
                <option value="name">按名称排序</option>
              </Select>
              <button
                type="button"
                onClick={() => setOnlyOnline((v) => !v)}
                aria-pressed={onlyOnline}
                className={cn(
                  'h-8 rounded-sm border px-3 font-display text-[11px] font-semibold uppercase tracking-[0.12em] transition-colors',
                  onlyOnline
                    ? 'border-primary bg-primary/15 text-primary shadow-neon-sm'
                    : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground',
                )}
              >
                仅看在线
              </button>
            </div>
          </div>
          <CardDescription>
            共 {filtered.length} 个客户端 · 数据来自 SSE 推送，断线时自动重连
          </CardDescription>
        </CardHeader>
        <CardContent>
          {peersQuery.isLoading ? (
            <TableSkeleton rows={6} />
          ) : peersQuery.isError ? (
            <ErrorBox message={(peersQuery.error as ApiError).message} />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={Signal}
              title={onlyOnline ? '当前没有在线客户端' : '没有匹配的客户端'}
              description={onlyOnline ? '客户端接入后会自动出现在这里。' : '换个关键词试试。'}
            />
          ) : (
            <div className="overflow-x-auto border border-border/60">
              <table className="hud-table min-w-[760px]">
                <caption className="sr-only">客户端实时状态列表</caption>
                <thead>
                  <tr>
                    <th scope="col" className="text-left">状态</th>
                    <th scope="col" className="text-left">名称</th>
                    <th scope="col" className="text-left">地址</th>
                    <th scope="col" className="text-right">下行速率</th>
                    <th scope="col" className="text-right">上行速率</th>
                    <th scope="col" className="text-right">累计下行</th>
                    <th scope="col" className="text-right">累计上行</th>
                    <th scope="col" className="text-left">握手</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map(({ peer, stats }) => (
                    <tr key={peer.id}>
                      <td>
                        <span className="flex items-center gap-2">
                          <StatusDot online={stats.online} />
                          <span className="font-display text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                            {stats.online ? '在线' : '离线'}
                          </span>
                        </span>
                      </td>
                      <td>
                        <p className="text-sm font-medium">{peer.name}</p>
                        {peer.tags.length > 0 && (
                          <div className="mt-1 flex flex-wrap gap-1">
                            {peer.tags.map((tag) => (
                              <Badge key={tag} variant="muted">
                                {tag}
                              </Badge>
                            ))}
                          </div>
                        )}
                      </td>
                      <td className="font-mono text-xs">{peer.address}</td>
                      <td className="text-right font-mono text-xs tabular-nums text-primary">
                        {formatRate(stats.rxRateBps)}
                      </td>
                      <td className="text-right font-mono text-xs tabular-nums text-neon-cyan">
                        {formatRate(stats.txRateBps)}
                      </td>
                      <td className="text-right font-mono text-xs tabular-nums">
                        {formatBytes(stats.rxBytes)}
                      </td>
                      <td className="text-right font-mono text-xs tabular-nums">
                        {formatBytes(stats.txBytes)}
                      </td>
                      <td className="font-mono text-[11px] text-muted-foreground">
                        <span className="flex items-center gap-1">
                          <Clock className="h-3 w-3" aria-hidden="true" />
                          {stats.online && stats.connectedSeconds > 0
                            ? `已连接 ${formatDuration(stats.connectedSeconds)}`
                            : formatRelative(firstMeaningful(stats.lastHandshake, peer.lastSeenAt))}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

/** 合并列表接口与 SSE 帧的统计信息。SSE 帧优先，缺失时回退到列表数据。 */
function mergeStats(
  peers: Peer[],
  streamed: PeerStats[] | undefined,
): { peer: Peer; stats: PeerStats }[] {
  const live = new Map(streamed?.map((s) => [s.peerId, s]) ?? [])
  return peers.map((peer) => ({
    peer,
    stats: live.get(peer.id) ?? peer.stats ?? emptyStats(peer.id, peer.publicKey),
  }))
}

function emptyStats(peerId: number, publicKey: string): PeerStats {
  return {
    peerId,
    publicKey,
    online: false,
    allowedIps: [],
    rxBytes: 0,
    txBytes: 0,
    rxRateBps: 0,
    txRateBps: 0,
    lastHandshake: '',
    connectedSince: '',
    connectedSeconds: 0,
  }
}

function Gauge({
  icon: Icon,
  label,
  percent,
  detail,
}: {
  icon: typeof Activity
  label: string
  percent: number
  detail: string
}) {
  const barClass =
    percent > 90
      ? 'bg-destructive shadow-[0_0_8px_hsl(var(--destructive)/0.7)]'
      : percent > 70
        ? 'bg-warning shadow-[0_0_8px_hsl(var(--warning)/0.7)]'
        : 'bg-primary shadow-[0_0_8px_hsl(var(--primary)/0.7)]'

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between text-sm">
        <span className="flex items-center gap-2">
          <Icon className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          {label}
        </span>
        <span className="font-mono text-xs tabular-nums text-muted-foreground">{detail}</span>
      </div>
      <SegmentedMeter percent={percent} barClass={barClass} />
    </div>
  )
}

/**
 * 键值行：上下行速率用不同霓虹色区分方向，
 * 与图表里的黄=下行 / 青=上行保持同一套映射。
 */
function Row({ label, value, cyan }: { label: string; value: string; cyan?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="text-muted-foreground">{label}</span>
      <span
        className={cn(
          'truncate font-mono tabular-nums',
          cyan ? 'text-neon-cyan' : 'text-primary',
        )}
      >
        {value}
      </span>
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