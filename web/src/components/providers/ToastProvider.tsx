import { createContext, useCallback, useContext, useMemo, useRef, useState } from 'react'
import { CheckCircle2, Info, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'

type ToastKind = 'success' | 'error' | 'info'

interface Toast {
  id: number
  kind: ToastKind
  message: string
}

interface ToastApi {
  success: (message: string) => void
  error: (message: string) => void
  info: (message: string) => void
}

const ToastContext = createContext<ToastApi | null>(null)

const KIND_STYLE: Record<ToastKind, string> = {
  success: 'border-success/50 bg-success/10 text-foreground shadow-neon-sm before:bg-success',
  error: 'border-destructive/50 bg-destructive/10 text-foreground shadow-neon-alert before:bg-destructive',
  info: 'border-primary/40 bg-card/90 text-foreground shadow-neon-sm before:bg-primary',
}

/** Toast 容器：所有操作反馈统一出口，自动消失且可被读屏器播报。 */
export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const nextId = useRef(1)

  const push = useCallback((kind: ToastKind, message: string) => {
    const id = nextId.current++
    setToasts((prev) => [...prev, { id, kind, message }])
    window.setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id))
    }, 4000)
  }, [])

  const value = useMemo<ToastApi>(
    () => ({
      success: (m) => push('success', m),
      error: (m) => push('error', m),
      info: (m) => push('info', m),
    }),
    [push],
  )

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className="pointer-events-none fixed inset-x-0 bottom-0 z-[60] flex flex-col items-center gap-2 p-4 sm:items-end"
        role="status"
        aria-live="polite"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            className={cn(
              'pointer-events-auto relative flex w-full max-w-sm items-start gap-2.5 overflow-hidden rounded-sm border bg-card/95 p-3 pl-4 text-sm backdrop-blur-md animate-slide-up',
              'before:absolute before:inset-y-0 before:left-0 before:w-[3px]',
              KIND_STYLE[t.kind],
            )}
          >
            {t.kind === 'success' && <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-success" />}
            {t.kind === 'error' && <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />}
            {t.kind === 'info' && <Info className="mt-0.5 h-4 w-4 shrink-0 text-primary" />}
            <span className="min-w-0 flex-1 break-words">{t.message}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used inside ToastProvider')
  return ctx
}