import { forwardRef } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

/**
 * 按钮：2077 的按钮是一块被斜切过的金属片，主操作是实心黄，
 * 次级操作只有一道发光描边，破坏性操作用品红。
 *
 * 所有变体共用同一个右上角斜切（clip-path），
 * 这样一排按钮的切角方向一致，整排看起来像同一块面板上切下来的。
 */
const buttonVariants = cva(
  [
    'relative inline-flex select-none items-center justify-center gap-2 whitespace-nowrap',
    'font-display text-xs font-semibold uppercase tracking-[0.12em]',
    'transition-[background-color,color,box-shadow,transform] duration-150',
    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
    'disabled:pointer-events-none disabled:opacity-40',
    'active:translate-y-px',
    // 右上角斜切：所有变体共用同一形状，一排按钮像从同一块面板上切下来的。
    // 放在 base 里而不是组件内，是为了让直接引用 buttonVariants() 的 <Link> 也带上切角。
    '[clip-path:polygon(7px_0,100%_0,100%_calc(100%_-_5px),calc(100%_-_5px)_100%,0_100%,0_7px)]',
  ],
  {
    variants: {
      variant: {
        // 实心黄：页面里同一屏只应有一个，其余用 outline，避免视觉全被黄色吃掉。
        default:
          'bg-primary text-primary-foreground hover:bg-primary/85 hover:shadow-neon-md shadow-neon-sm',
        destructive:
          'bg-destructive text-destructive-foreground hover:bg-destructive/85 hover:shadow-neon-alert',
        outline:
          'border border-border bg-transparent text-foreground hover:border-primary/70 hover:text-primary hover:shadow-neon-sm',
        secondary:
          'bg-secondary text-secondary-foreground hover:bg-accent hover:text-accent-foreground',
        ghost: 'text-muted-foreground hover:bg-accent hover:text-foreground',
        link: 'text-primary underline-offset-4 hover:text-primary/80 hover:underline',
      },
      size: {
        default: 'h-9 px-4 py-2',
        sm: 'h-8 px-3 text-[11px]',
        lg: 'h-11 px-7 text-sm',
        icon: 'h-9 w-9',
        'icon-sm': 'h-8 w-8',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, type = 'button', ...props }, ref) => (
    <button
      ref={ref}
      type={type}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  ),
)
Button.displayName = 'Button'

export { buttonVariants }
