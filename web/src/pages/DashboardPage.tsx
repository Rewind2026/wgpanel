import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  Cpu,
  Gauge,
  HardDrive,
  MemoryStick,
  Radio,
  Users,
} from 'lucide-react'
import { api } from '@/lib/api'
import { useMetricsStream } from '@/lib/useMetricsStream'
import { formatBytes, formatDuration, formatRate } from '@/lib/utils'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/Card'
import { Badge, StatusDot } from '@/components/ui/Badge'
import { EmptyState, StatTileSkeleton, TableSkeleton } from '@/components/ui/Skeleton'
import { SegmentedMeter, StatTile, Sparkline } from '@/components/ui/StatTile'

export function DashboardPage() {
  const { snapshot, connected } = useMetricsStream(true)

  const peersQuery = useQuery({
    queryKey: ['peers'],
    queryFn: ({ signal }) => api.listPeers(signal),
    refetchInterval: 15_000,
  })

  const peers = peersQuery.data ?? []
  const onlineCount = snapshot?.tunnel.onlinePeers ?? peers.filter((p) => p.stats?.online).length

  // 累积 CPU 历史序列供 Sparkline 使用。
  //
  // 之前这里直接写死 points={[cpuPercent, memPercent]}：两个点画出来是一条
  // 从 CPU 斜拉到内存的直线，既不是任何一方的趋势，还会让读者以为
  // 「内存是 CPU 的下一阶段」。必须按时间累积同一指标的历史值才有意义。
  const [cpuHistory, setCpuHistory] = useState<number[]>([])
  const lastSampleRef = useRef('')
  useEffect(() => {
    if (!snapshot) return
    // 同一帧只采一次。SSE 断线重连会立刻补发快照，
    // 不去重的话重连一次就在曲线上叠出一串重复点。
    const frame = snapshot.tunnel.collectedAt
    if (frame === lastSampleRef.current) return
    lastSampleRef.current = frame
    setCpuHistory((prev) => [...prev, snapshot.system.cpuPercent].slice(-30))
  }, [snapshot])

  return (
    <div className="space-y-5">
      {/* 页头：代号 + 标题 + 链路状态 */}
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// OVR-01</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              仪表盘
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">服务器状态与隧道流量总览</p>
          </div>
          <Badge variant={connected ? 'success' : 'destructive'}>
            <StatusDot online={connected} />
            {connected ? '实时推送已连接' : '推送断开，重连中'}
          </Badge>
        </div>
        <div className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80" aria-hidden="true" />
      </header>

      {/* 指标卡 */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {snapshot ? (
          <>
            <StatTile
              icon={Users}
              label="在线客户端"
              value={`${onlineCount} / ${snapshot.tunnel.totalPeers}`}
              hint={snapshot.tunnel.interfaceUp ? '接口正常' : '接口未就绪'}
              accent={snapshot.tunnel.interfaceUp ? 'success' : 'warning'}
              trend={snapshot.tunnel.totalPeers > 0 ? (onlineCount / snapshot.tunnel.totalPeers) * 100 : 0}
            />
            <StatTile
              icon={ArrowDownToLine}
              label="下行速率"
              value={formatRate(snapshot.tunnel.rxRateBps)}
              hint={`累计 ${formatBytes(snapshot.tunnel.rxBytes)}`}
            />
            <StatTile
              icon={ArrowUpFromLine}
              label="上行速率"
              value={formatRate(snapshot.tunnel.txRateBps)}
              hint={`累计 ${formatBytes(snapshot.tunnel.txBytes)}`}
            />
            <StatTile
              icon={Radio}
              label="监听端口"
              value={String(snapshot.tunnel.listenPort || '—')}
              hint={snapshot.tunnel.publicEndpoint || `接口 ${snapshot.tunnel.interface}`}
              accent="primary"
            />
          </>
        ) : (
          Array.from({ length: 4 }).map((_, i) => <StatTileSkeleton key={i} />)
        )}
      </div>

      <div className="grid gap-5 lg:grid-cols-3">
        {/* 资源占用 */}
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>资源占用</CardTitle>
            <CardDescription>容器与宿主机实时指标</CardDescription>
          </CardHeader>
          <CardContent>
            {snapshot ? (
              <div className="grid gap-5 sm:grid-cols-2">
                <ResourceRow
                  icon={Cpu}
                  label="CPU"
                  percent={snapshot.system.cpuPercent}
                  detail={`${snapshot.system.cpuPercent.toFixed(1)}%`}
                />
                <ResourceRow
                  icon={MemoryStick}
                  label="内存"
                  percent={snapshot.system.memPercent}
                  detail={`${formatBytes(snapshot.system.memUsedBytes)} · ${snapshot.system.memPercent.toFixed(1)}%`}
                />
                <ResourceRow
                  icon={HardDrive}
                  label="磁盘"
                  percent={snapshot.system.diskPercent}
                  detail={`${formatBytes(snapshot.system.diskUsedBytes)} · ${snapshot.system.diskPercent.toFixed(1)}%`}
                />
                {/* 网卡没有「占用率」这个量纲，只给速率，不画进度条 */}
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between text-sm">
                    <span className="flex items-center gap-2">
                      <Activity className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                      网卡
                    </span>
                    <span className="font-mono text-xs tabular-nums text-neon-cyan">
                      ↓ {formatRate(snapshot.system.netRxBps)} · ↑{' '}
                      {formatRate(snapshot.system.netTxBps)}
                    </span>
                  </div>
                  <div className="flex h-2 items-end gap-[2px]" aria-hidden="true">
                    <NetSpark up={false} bytes={snapshot.system.netRxBps} />
                    <NetSpark up bytes={snapshot.system.netTxBps} />
                  </div>
                </div>
              </div>
            ) : (
              <TableSkeleton rows={4} />
            )}
          </CardContent>
        </Card>

        {/* 运行时 */}
        <Card>
          <CardHeader>
            <CardTitle>运行时</CardTitle>
            <CardDescription>容器与调谐状态</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <RuntimeRow label="运行时长" value={formatDuration(snapshot?.system.uptimeSeconds ?? 0)} />
            <RuntimeRow label="协程数" value={String(snapshot?.system.goroutines ?? 0)} />
            <RuntimeRow label="接口" value={snapshot?.tunnel.interface ?? '—'} />
            <RuntimeRow
              label="状态"
              value={snapshot?.tunnel.interfaceUp ? '运行中' : '未就绪'}
              accent={snapshot?.tunnel.interfaceUp}
            />
            <RuntimeRow
              label="总流量"
              value={formatBytes((snapshot?.tunnel.rxBytes ?? 0) + (snapshot?.tunnel.txBytes ?? 0))}
            />
            <div className="space-y-1.5 border-t border-border/60 pt-3">
              <p className="hud-label text-muted-foreground">
                CPU 趋势 · 最近 {cpuHistory.length} 次采样
              </p>
              <Sparkline points={cpuHistory} />
            </div>
          </CardContent>
        </Card>
      </div>

      {/* 客户端流量榜 */}
      <Card>
        <CardHeader>
          <CardTitle>客户端流量</CardTitle>
          <CardDescription>按累计下行流量排序的前 5 名</CardDescription>
        </CardHeader>
        <CardContent>
          {peersQuery.isLoading ? (
            <TableSkeleton rows={5} />
          ) : peers.length === 0 ? (
            <EmptyState
              icon={Users}
              title="还没有客户端"
              description="创建第一个客户端后，它会立即出现在这里并可扫码导入。"
            />
          ) : (
            <div className="space-y-2">
              {peers
                .slice()
                .sort((a, b) => (b.stats?.rxBytes ?? 0) - (a.stats?.rxBytes ?? 0))
                .slice(0, 5)
                .map((peer, idx) => (
                  <div
                    key={peer.id}
                    className="flex items-center gap-3 border border-border/60 bg-background/40 px-3 py-2 transition-colors hover:border-primary/50 hover:bg-primary/5"
                  >
                    <span className="hud-value w-5 text-xs text-muted-foreground">
                      {String(idx + 1).padStart(2, '0')}
                    </span>
                    <StatusDot online={peer.stats?.online ?? false} />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{peer.name}</p>
                      <p className="truncate font-mono text-[11px] text-muted-foreground">
                        {peer.address}
                      </p>
                    </div>
                    <div className="text-right">
                      <p className="font-mono text-sm tabular-nums text-primary">
                        ↓ {formatBytes(peer.stats?.rxBytes ?? 0)}
                      </p>
                      <p className="font-mono text-[11px] tabular-nums text-neon-cyan">
                        ↑ {formatBytes(peer.stats?.txBytes ?? 0)}
                      </p>
                    </div>
                  </div>
                ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

/**
 * 资源行：分段式仪表条而不是连续进度条。
 *
 * 分段的好处是低占用时也能看出「有几格亮了」——
 * 连续条在 2% 时只有 2px 宽，等于没有反馈。
 */
function ResourceRow({
  icon: Icon,
  label,
  percent,
  detail,
}: {
  icon: typeof Gauge
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

/** 网卡速率的迷你条：没有百分比刻度，用条长表示相对速率，仅作视觉提示。 */
function NetSpark({ bytes, up }: { bytes: number; up?: boolean }) {
  // 100 MB/s 作为满格参考，让常见量级（几百 KB/s ~ 几 MB/s）有可读的条长变化。
  const ratio = Math.min(1, bytes / (100 * 1024 * 1024))
  return (
    <span
      className={
        up
          ? 'h-full flex-1 bg-neon-cyan/70 transition-[width] duration-500'
          : 'h-1/2 flex-1 bg-primary/70 transition-[width] duration-500'
      }
      style={{ width: `${Math.max(3, ratio * 100)}%` }}
    />
  )
}

function RuntimeRow({
  label,
  value,
  accent,
}: {
  label: string
  value: string
  /** 正常状态用绿色点标出，避免「运行中」只是一行普通灰字。 */
  accent?: boolean
}) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="text-muted-foreground">{label}</span>
      <span className="flex items-center gap-1.5 truncate font-mono tabular-nums">
        {accent && <span className="h-1.5 w-1.5 bg-success shadow-[0_0_8px_hsl(var(--success)/0.9)]" />}
        {value}
      </span>
    </div>
  )
}
