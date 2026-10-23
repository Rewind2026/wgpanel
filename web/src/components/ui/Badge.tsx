import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

/**
 * 状态标签：2077 的标签是嵌在面板上的小色块，不是圆角胶囊。
 * 统一用极小圆角 + 细描边 + 同色半透明底，
 * 让「在线 / 离线 / 告警」在扫视时靠色块形状就能区分。
 */
const badgeVariants = cva(
  'inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 font-display text-[10px] font-semibold uppercase tracking-[0.14em] leading-5 transition-colors',
  {
    variants: {
      variant: {
        default: 'border-primary/50 bg-primary/15 text-primary',
        secondary: 'border-border bg-secondary text-secondary-foreground',
        outline: 'border-border bg-transparent text-foreground',
        success: 'border-success/50 bg-success/15 text-success',
        warning: 'border-warning/50 bg-warning/15 text-warning',
        destructive: 'border-destructive/50 bg-destructive/15 text-destructive',
        muted: 'border-border bg-muted text-muted-foreground',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />
}

/**
 * 在线状态点：方块而非圆点，呼应整体直角语言。
 * 在线时带一圈扩散的边框脉冲，扫视页面时能立刻定位到「有流量」的位置。
 */
export function StatusDot({ online }: { online: boolean }) {
  return (
    <span className="relative inline-flex h-2.5 w-2.5 shrink-0" aria-label={online ? '在线' : '离线'}>
      {online && (
        <span className="absolute inline-flex h-full w-full animate-ping bg-success/50 [clip-path:polygon(0_0,100%_0,100%_100%,0_100%)]" />
      )}
      <span
        className={cn(
          'relative inline-flex h-2.5 w-2.5',
          online ? 'bg-success shadow-[0_0_8px_hsl(var(--success)/0.8)]' : 'bg-muted-foreground/30',
        )}
      />
    </span>
  )
}
