import { cn } from '@/lib/utils'

/** 骨架块：一道横向扫过的亮条，模拟数据正在流入。 */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div className={cn('relative overflow-hidden rounded-sm bg-muted', className)} aria-hidden="true">
      <div className="absolute inset-0 -translate-x-full animate-shimmer bg-gradient-to-r from-transparent via-primary/15 to-transparent" />
    </div>
  )
}

/** 统计卡骨架，首屏加载时替代真实数据。 */
export function StatTileSkeleton() {
  return (
    <div className="stat-tile space-y-3">
      <Skeleton className="h-3 w-20" />
      <Skeleton className="h-7 w-28" />
      <div className="flex h-2 gap-[2px]">
        {Array.from({ length: 20 }).map((_, i) => (
          <Skeleton key={i} className="flex-1" />
        ))}
      </div>
    </div>
  )
}

/** 表格骨架，行数按预期展示行数传入，避免加载完成时布局跳动。 */
export function TableSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  )
}

/**
 * 空状态：不是「这里没有数据」的友好提示，
 * 而是像设备返回了一条空结果——切角方框 + 状态码 + 一句冷冰冰的说明。
 */
export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
}: {
  icon?: React.ComponentType<{ className?: string }>
  title: string
  description?: string
  action?: React.ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 px-6 py-16 text-center">
      {Icon && (
        <div className="relative border border-border bg-muted/40 p-4 [clip-path:polygon(10px_0,100%_0,100%_calc(100%_-_10px),calc(100%_-_10px)_100%,0_100%,0_10px)]">
          <Icon className="h-6 w-6 text-muted-foreground" />
        </div>
      )}
      <div className="space-y-1.5">
        <p className="font-display text-sm font-semibold uppercase tracking-[0.14em]">{title}</p>
        {description && (
          <p className="mx-auto max-w-sm text-xs leading-relaxed text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {action}
    </div>
  )
}
