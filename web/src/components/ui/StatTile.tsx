import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Card } from './Card'

/**
 * 指标卡：图标 + 标签 + 数值 + 副信息。
 *
 * 数值用等宽字体加霓虹外发光，标签用极小的展示体大写字距，
 * 中间夹一条分段式进度条——分段而不是连续填充，
 * 是为了让「占用率」读起来像仪表刻度而不是一根进度条。
 */
export function StatTile({
  icon: Icon,
  label,
  value,
  hint,
  trend,
  accent = 'primary',
}: {
  icon: LucideIcon
  label: string
  value: string
  hint?: string
  /** 副进度条，取值 0~100。 */
  trend?: number
  accent?: 'primary' | 'success' | 'warning' | 'destructive'
}) {
  const accentText = {
    primary: 'text-primary',
    success: 'text-success',
    warning: 'text-warning',
    destructive: 'text-destructive',
  }[accent]

  const accentBar = {
    primary: 'bg-primary shadow-[0_0_8px_hsl(var(--primary)/0.7)]',
    success: 'bg-success shadow-[0_0_8px_hsl(var(--success)/0.7)]',
    warning: 'bg-warning shadow-[0_0_8px_hsl(var(--warning)/0.7)]',
    destructive: 'bg-destructive shadow-[0_0_8px_hsl(var(--destructive)/0.7)]',
  }[accent]

  return (
    <Card className="stat-tile flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <span className="hud-label text-muted-foreground">{label}</span>
        <Icon className={cn('h-4 w-4 shrink-0', accentText)} aria-hidden="true" />
      </div>
      <div className="hud-value text-xl font-bold leading-none text-foreground sm:text-2xl">{value}</div>
      {trend !== undefined && <SegmentedMeter percent={trend} barClass={accentBar} />}
      {hint && <span className="font-mono text-[11px] text-muted-foreground">{hint}</span>}
    </Card>
  )
}

/**
 * 分段式仪表条：把 0~100 切成 20 段。
 * 已填充段用强调色并发光，未填充段只有极淡的轮廓，
 * 这样即使只有 3% 也能看出「有一点负载」，而不是一条看不见的细线。
 */
export function SegmentedMeter({
  percent,
  barClass,
  segments = 20,
}: {
  percent: number
  barClass: string
  segments?: number
}) {
  const filled = Math.round((Math.min(100, Math.max(0, percent)) / 100) * segments)
  return (
    <div
      className="flex h-2 w-full gap-[2px]"
      role="meter"
      aria-valuenow={Math.round(percent)}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      {Array.from({ length: segments }).map((_, i) => (
        <span
          key={i}
          className={cn(
            'flex-1 transition-colors duration-300',
            i < filled ? barClass : 'bg-border/50',
          )}
        />
      ))}
    </div>
  )
}

/** 迷你走势图：折线 + 渐变填充，线本身带霓虹描边。 */
export function Sparkline({ points }: { points: number[] }) {
  if (points.length < 2) return null
  const max = Math.max(...points, 1)
  const min = Math.min(...points, 0)
  const range = max - min || 1
  const path = points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * 100
      const y = 30 - ((p - min) / range) * 28
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`
    })
    .join(' ')

  return (
    <svg
      viewBox="0 0 100 30"
      preserveAspectRatio="none"
      className="h-8 w-full"
      role="img"
      aria-label="趋势曲线"
    >
      <defs>
        <linearGradient id="sparkline-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity="0.35" />
          <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={`${path} L100,30 L0,30 Z`} fill="url(#sparkline-fill)" />
      <path
        d={path}
        fill="none"
        stroke="hsl(var(--primary))"
        strokeWidth="1.5"
        vectorEffect="non-scaling-stroke"
        style={{ filter: 'drop-shadow(0 0 3px hsl(var(--primary) / 0.8))' }}
      />
    </svg>
  )
}
