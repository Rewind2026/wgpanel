import { useCallback, useEffect, useRef } from 'react'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from './Button'

/**
 * 对话框容器：遮罩点击与 Esc 关闭、焦点陷阱、关闭后焦点归还触发元素。
 *
 * 这些行为用原生 <dialog> 很难同时满足移动端样式与焦点管理，
 * 因此统一在这里实现，各个页面只关心内容。
 */
export function Dialog({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  className,
}: {
  open: boolean
  title: string
  description?: string
  onClose: () => void
  children: React.ReactNode
  footer?: React.ReactNode
  className?: string
}) {
  const panelRef = useRef<HTMLDivElement>(null)
  const restoreRef = useRef<HTMLElement | null>(null)

  // 打开时记住当前焦点，关闭后归还，否则键盘用户会被丢回页面顶部。
  useEffect(() => {
    if (!open) return
    restoreRef.current = document.activeElement as HTMLElement | null
    const first = panelRef.current?.querySelector<HTMLElement>(
      'input:not([type="hidden"]), select, textarea, button, [href], [tabindex]:not([tabindex="-1"])',
    )
    first?.focus()
    return () => restoreRef.current?.focus?.()
  }, [open])

  // 焦点陷阱：Tab 在最后一个可聚焦元素上回到第一个。
  const trapFocus = useCallback((e: KeyboardEvent) => {
    if (e.key !== 'Tab') return
    const panel = panelRef.current
    if (!panel) return
    const nodes = Array.from(
      panel.querySelectorAll<HTMLElement>(
        'input:not([type="hidden"]):not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ),
    ).filter((el) => el.offsetParent !== null)
    if (nodes.length === 0) return
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }, [])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
      trapFocus(e)
    }
    document.addEventListener('keydown', onKey)
    // 背景内容对读屏器隐藏，避免键盘 Tab 到遮罩后面。
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose, trapFocus])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center sm:items-center sm:p-4">
      <button
        type="button"
        aria-label="关闭对话框"
        tabIndex={-1}
        onClick={onClose}
        className="absolute inset-0 bg-background/80 backdrop-blur-sm"
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={cn(
          // panel-sheet：窄屏取消斜切（详见 index.css），sm 以上恢复面板切角。
          'glass-card panel-sheet animate-slide-up relative z-10 flex max-h-[90vh] w-full flex-col overflow-hidden sm:max-w-lg',
          className,
        )}
      >
        {/* 顶部扫描亮线，暗示「面板已激活」 */}
        <div className="hazard-stripes h-[3px] w-full shrink-0 opacity-80" aria-hidden="true" />

        <div className="flex items-start gap-3 border-b border-border/60 p-4 sm:p-5">
          <div className="min-w-0 flex-1">
            <h2 className="font-display text-sm font-semibold uppercase tracking-[0.14em]">{title}</h2>
            {description && (
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{description}</p>
            )}
          </div>
          <Button variant="ghost" size="icon-sm" aria-label="关闭" onClick={onClose}>
            <X className="h-4 w-4" />
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-5">{children}</div>

        {footer && (
          <div className="flex justify-end gap-2 border-t border-border/60 p-4 sm:p-5">
            {footer}
          </div>
        )}
      </div>
    </div>
  )
}

/** 需要二次确认的破坏性操作确认框。 */
export function ConfirmDialog({
  open,
  title,
  message,
  confirmText = '确认',
  destructive = true,
  submitting = false,
  onConfirm,
  onClose,
}: {
  open: boolean
  title: string
  message: string
  confirmText?: string
  destructive?: boolean
  submitting?: boolean
  onConfirm: () => void
  onClose: () => void
}) {
  return (
    <Dialog
      open={open}
      title={title}
      onClose={onClose}
      className="sm:max-w-md"
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={submitting}>
            取消
          </Button>
          <Button
            variant={destructive ? 'destructive' : 'default'}
            onClick={onConfirm}
            disabled={submitting}
          >
            {confirmText}
          </Button>
        </>
      }
    >
      <p className="text-sm text-muted-foreground">{message}</p>
    </Dialog>
  )
}
