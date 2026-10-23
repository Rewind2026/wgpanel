import { forwardRef } from 'react'
import { cn } from '@/lib/utils'

/**
 * 表单控件：内凹的深色槽 + 左侧黄色定位条 + 聚焦时整条槽亮起。
 * 聚焦反馈做得比常规更强（边框变黄 + 外发光），
 * 因为这套界面底色极暗，弱聚焦环在暗背景上几乎看不见。
 */
const FIELD_BASE = [
  'w-full rounded-sm border border-input bg-background/70 px-3 py-1 text-sm',
  'text-foreground placeholder:text-muted-foreground/70',
  'transition-[border-color,box-shadow,background-color] duration-150',
  'focus-visible:border-primary/70 focus-visible:bg-background focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50 focus-visible:shadow-neon-sm',
  'disabled:cursor-not-allowed disabled:opacity-50',
].join(' ')

export const Input = forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  ({ className, type = 'text', ...props }, ref) => (
    <input ref={ref} type={type} className={cn(FIELD_BASE, 'h-9', className)} {...props} />
  ),
)
Input.displayName = 'Input'

export const Label = forwardRef<HTMLLabelElement, React.LabelHTMLAttributes<HTMLLabelElement>>(
  ({ className, ...props }, ref) => (
    <label
      ref={ref}
      className={cn(
        'font-display text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground',
        className,
      )}
      {...props}
    />
  ),
)
Label.displayName = 'Label'

export const Select = forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, children, ...props }, ref) => (
    <select ref={ref} className={cn(FIELD_BASE, 'h-9 cursor-pointer pr-8', className)} {...props}>
      {children}
    </select>
  ),
)
Select.displayName = 'Select'

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  React.TextareaHTMLAttributes<HTMLTextAreaElement>
>(({ className, ...props }, ref) => (
  <textarea ref={ref} className={cn(FIELD_BASE, 'min-h-[80px] py-2', className)} {...props} />
))
Textarea.displayName = 'Textarea'

/** 带标签与可选说明的表单字段容器，统一间距与错误提示位置。 */
export function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
  className,
}: {
  label: string
  hint?: string
  error?: string
  htmlFor?: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : hint ? (
        <p className="text-xs text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  )
}

/**
 * 开关控件：滑块从方形轨道左端滑到右端，滑块本身是切角块。
 * 开启时整条轨道亮黄并发光，2077 里「通电」状态就该是这样。
 */
export function Switch({
  checked,
  onCheckedChange,
  disabled,
  label,
}: {
  checked: boolean
  onCheckedChange: (next: boolean) => void
  disabled?: boolean
  label?: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onCheckedChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'bg-primary shadow-neon-sm' : 'bg-input',
      )}
    >
      <span
        className={cn(
          'inline-block h-4 w-4 transform bg-primary-foreground transition-transform',
          checked ? 'translate-x-[18px]' : 'translate-x-0.5',
          '[clip-path:polygon(3px_0,100%_0,100%_calc(100%_-_3px),calc(100%_-_3px)_100%,0_100%,0_3px)]',
        )}
      />
    </button>
  )
}
