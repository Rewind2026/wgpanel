import { useMemo } from 'react'
import { cn, formatBytes, formatRate } from '@/lib/utils'
import type { TrafficPoint } from '@/types/api'

/**
 * 流量折线图（纯 SVG，无第三方图表库）。
 *
 * 选择手写 SVG 而不是引入 ECharts：这里的图形只有一个折线 + 坐标轴 + hover，
 * 一整个图表库换不来可维护性收益，反而会让 bundle 膨胀、并引入自己的主题体系。
 */

const VIEW_W = 1000
const VIEW_H = 260
const PAD_LEFT = 56
const PAD_RIGHT = 12
const PAD_TOP = 12
const PAD_BOTTOM = 26

/** 纵轴刻度：挑一个 1/2/5×10^n 的整数值，避免出现 3.7 MB/s 这种轴标签。 */
function niceCeil(value: number): number {
  if (value <= 0) return 1
  const exp = Math.floor(Math.log10(value))
  const base = Math.pow(10, exp)
  const norm = value / base
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10
  return step * base
}

function axisLabel(value: number): string {
  if (value >= 1024 * 1024 * 1024) return `${(value / 1024 / 1024 / 1024).toFixed(1)}G`
  if (value >= 1024 * 1024) return `${(value / 1024 / 1024).toFixed(0)}M`
  if (value >= 1024) return `${Math.round(value / 1024)}K`
  return String(Math.round(value))
}

export function TrafficChart({
  points,
  fromTs,
  toTs,
  height = 220,
}: {
  points: TrafficPoint[]
  /** 时间轴范围，来自响应里的 from/to；用真实窗口而不是数据首尾，
   *  这样窗口内没有采样的两端也能正确显示留白。 */
  fromTs: number
  toTs: number
  height?: number
}) {
  const model = useMemo(() => buildModel(points, fromTs, toTs), [points, fromTs, toTs])

  if (points.length === 0) {
    return (
      <div
        className="flex items-center justify-center border border-dashed border-border text-sm text-muted-foreground"
        style={{ height }}
      >
        该时间范围内还没有采样数据
      </div>
    )
  }

  return (
    <figure className="space-y-3">
      <svg
        viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
        preserveAspectRatio="none"
        style={{ height }}
        className="w-full"
        role="img"
        aria-label="流量历史曲线"
      >
        <defs>
          {/* 下行用主色黄，上行用辅助霓虹青：两色在黑底上都极亮，
              且黄/青在色觉障碍下仍可区分（明度差大），不只靠色相。 */}
          <linearGradient id="traffic-rx-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity="0.32" />
            <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity="0" />
          </linearGradient>
          <linearGradient id="traffic-tx-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="hsl(var(--neon-cyan))" stopOpacity="0.22" />
            <stop offset="100%" stopColor="hsl(var(--neon-cyan))" stopOpacity="0" />
          </linearGradient>
          {/* 曲线辉光：比外发光滤镜便宜，且不会影响周围布局 */}
          <filter id="neon-glow" x="-20%" y="-40%" width="140%" height="180%">
            <feGaussianBlur stdDeviation="3" result="blur" />
            <feMerge>
              <feMergeNode in="blur" />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
        </defs>

        {/* 横向网格与纵轴刻度 */}
        {model.ticks.map((tick) => (
          <g key={tick.value}>
            <line
              x1={PAD_LEFT}
              x2={VIEW_W - PAD_RIGHT}
              y1={tick.y}
              y2={tick.y}
              stroke="hsl(var(--border))"
              strokeWidth="1"
              vectorEffect="non-scaling-stroke"
              strokeDasharray="3 4"
            />
            <text
              x={PAD_LEFT - 8}
              y={tick.y + 3}
              textAnchor="end"
              className="fill-muted-foreground font-mono"
              fontSize="10"
            >
              {axisLabel(tick.value)}
            </text>
          </g>
        ))}

        <path d={model.rxArea} fill="url(#traffic-rx-fill)" />
        <path
          d={model.rxPath}
          stroke="hsl(var(--primary))"
          strokeWidth="2"
          fill="none"
          vectorEffect="non-scaling-stroke"
          filter="url(#neon-glow)"
        />
        <path d={model.txArea} fill="url(#traffic-tx-fill)" />
        <path
          d={model.txPath}
          stroke="hsl(var(--neon-cyan))"
          strokeWidth="2"
          fill="none"
          strokeDasharray="4 3"
          vectorEffect="non-scaling-stroke"
        />

        {/* 时间轴刻度 */}
        {model.timeTicks.map((tick) => (
          <text
            key={tick.label}
            x={tick.x}
            y={VIEW_H - 8}
            textAnchor="middle"
            className="fill-muted-foreground font-mono"
            fontSize="10"
          >
            {tick.label}
          </text>
        ))}
      </svg>

      <figcaption className="flex flex-wrap items-center gap-4 font-mono text-[11px] text-muted-foreground">
        <LegendItem className="bg-primary" label="下行速率" />
        <LegendItem className="bg-neon-cyan" label="上行速率" dashed />
        <span className="ml-auto">
          峰值 ↓ {formatRate(model.peakRx)} · ↑ {formatRate(model.peakTx)} · 合计流量{' '}
          {formatBytes(model.totalBytes)}
        </span>
      </figcaption>
    </figure>
  )
}

function LegendItem({
  className,
  label,
  dashed,
}: {
  className: string
  label: string
  dashed?: boolean
}) {
  return (
    <span className="flex items-center gap-1.5">
      <span
        className={cn('h-0.5 w-5', className)}
        style={
          dashed
            ? {
                backgroundImage:
                  'repeating-linear-gradient(90deg,currentColor 0 4px,transparent 4px 7px)',
              }
            : undefined
        }
        aria-hidden="true"
      />
      {label}
    </span>
  )
}

interface Model {
  rxPath: string
  txPath: string
  rxArea: string
  txArea: string
  ticks: { value: number; y: number }[]
  timeTicks: { label: string; x: number }[]
  peakRx: number
  peakTx: number
  totalBytes: number
}

function buildModel(points: TrafficPoint[], fromTs: number, toTs: number): Model {
  const peakRx = Math.max(...points.map((p) => p.rxRate), 0)
  const peakTx = Math.max(...points.map((p) => p.txRate), 0)
  const maxY = niceCeil(Math.max(peakRx, peakTx) * 1.15)

  const span = Math.max(1, toTs - fromTs)
  const plotW = VIEW_W - PAD_LEFT - PAD_RIGHT
  const plotH = VIEW_H - PAD_TOP - PAD_BOTTOM

  const x = (ts: number) => PAD_LEFT + ((ts - fromTs) / span) * plotW
  const y = (v: number) => PAD_TOP + plotH - (v / maxY) * plotH

  const rxPath = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.ts).toFixed(2)},${y(p.rxRate).toFixed(2)}`).join(' ')
  const txPath = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.ts).toFixed(2)},${y(p.txRate).toFixed(2)}`).join(' ')

  const baseline = PAD_TOP + plotH
  const rxArea = `${rxPath} L${x(points[points.length - 1].ts).toFixed(2)},${baseline} L${x(points[0].ts).toFixed(2)},${baseline} Z`
  const txArea = `${txPath} L${x(points[points.length - 1].ts).toFixed(2)},${baseline} L${x(points[0].ts).toFixed(2)},${baseline} Z`

  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => ({
    value: maxY * f,
    y: PAD_TOP + plotH - f * plotH,
  }))

  // 时间刻度固定 4 个均分点，标签按跨度选择分钟/小时/天精度。
  const timeTicks = [0, 1, 2, 3].map((i) => {
    const ratio = i / 3
    return {
      label: formatAxisTime(fromTs + span * ratio, span),
      x: PAD_LEFT + ratio * plotW,
    }
  })

  // 合计流量 = 窗口内累计计数器的正向增量之和。
  //
  // rx_bytes / tx_bytes 是内核累计计数器（后端聚合也用 MAX 而非 SUM），
  // 逐点求和会把同一份字节数重复计入每个采样点，一条 5 GB 的曲线
  // 配上 18 个采样点就会显示成 90 GB。
  //
  // 但也不能只取末值减首值：接口重建 / 容器重启会让计数器归零重新开始
  // （rateTracker.compute 对回退已经做了保护，界面不会显示负速率），
  // 此时末值反而小于首值，首尾差会被夹成 0，把归零前那 5 GB 凭空抹掉。
  // 逐段累加正向增量才能同时躲开这两种错法：只累计上升的差值，
  // 归零处贡献 0（那一段流量本来就无从得知），归零前的流量完整保留。
  let totalBytes = 0
  for (let i = 1; i < points.length; i++) {
    totalBytes += Math.max(0, points[i].rxBytes - points[i - 1].rxBytes)
    totalBytes += Math.max(0, points[i].txBytes - points[i - 1].txBytes)
  }

  return { rxPath, txPath, rxArea, txArea, ticks, timeTicks, peakRx, peakTx, totalBytes }
}

function formatAxisTime(ts: number, span: number): string {
  const d = new Date(ts * 1000)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  if (span > 3 * 86400) return `${d.getMonth() + 1}/${d.getDate()}`
  return `${hh}:${mm}`
}
