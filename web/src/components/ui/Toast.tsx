import { createContext, ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { CheckCircle2, AlertCircle, Info, AlertTriangle, X } from 'lucide-react'
import { cn } from './cn'

type Tone = 'success' | 'error' | 'warn' | 'info'

interface Toast {
  id: number
  tone: Tone
  title: string
  description?: string
}

interface ToastContextValue {
  show: (input: { tone?: Tone; title: string; description?: string; duration?: number }) => void
  success: (title: string, description?: string) => void
  error: (title: string, description?: string) => void
  warn: (title: string, description?: string) => void
  info: (title: string, description?: string) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

const ICONS: Record<Tone, ReactNode> = {
  success: <CheckCircle2 className="h-4 w-4 text-success" />,
  error: <AlertCircle className="h-4 w-4 text-danger" />,
  warn: <AlertTriangle className="h-4 w-4 text-warning" />,
  info: <Info className="h-4 w-4 text-accent" />,
}

const ACCENTS: Record<Tone, string> = {
  success: 'border-l-success',
  error: 'border-l-danger',
  warn: 'border-l-warning',
  info: 'border-l-accent',
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const seq = useRef(0)
  const timers = useRef(new Map<number, number>())

  const dismiss = useCallback((id: number) => {
    setToasts((cur) => cur.filter((t) => t.id !== id))
    const handle = timers.current.get(id)
    if (handle) {
      window.clearTimeout(handle)
      timers.current.delete(id)
    }
  }, [])

  const show = useCallback<ToastContextValue['show']>(
    ({ tone = 'info', title, description, duration = 4500 }) => {
      const id = ++seq.current
      setToasts((cur) => [...cur, { id, tone, title, description }])
      const handle = window.setTimeout(() => dismiss(id), duration)
      timers.current.set(id, handle)
    },
    [dismiss],
  )

  // Memoized so consumers can safely list `toast` in effect dependencies —
  // a fresh object every render used to tear down and rebuild polling
  // intervals whenever any toast fired.
  const value = useMemo<ToastContextValue>(
    () => ({
      show,
      success: (title, description) => show({ tone: 'success', title, description }),
      error: (title, description) => show({ tone: 'error', title, description, duration: 8000 }),
      warn: (title, description) => show({ tone: 'warn', title, description }),
      info: (title, description) => show({ tone: 'info', title, description }),
    }),
    [show],
  )

  useEffect(() => {
    const pending = timers.current
    return () => {
      pending.forEach((handle) => window.clearTimeout(handle))
      pending.clear()
    }
  }, [])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        aria-live="polite"
        className="pointer-events-none fixed inset-0 z-[60] flex flex-col items-end justify-end gap-2 p-4 sm:p-6"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            role="status"
            className={cn(
              'pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-lg border border-border-base bg-bg-surface px-4 py-3 shadow-lg animate-slide-in-right',
              'border-l-4',
              ACCENTS[t.tone],
            )}
          >
            <div className="pt-0.5">{ICONS[t.tone]}</div>
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-fg-base">{t.title}</div>
              {t.description && (
                <div className="mt-0.5 text-sm text-fg-muted">{t.description}</div>
              )}
            </div>
            <button
              onClick={() => dismiss(t.id)}
              aria-label="Dismiss notification"
              className="-mr-1 -mt-1 rounded p-0.5 text-fg-subtle transition-colors hover:bg-bg-raised hover:text-fg-base"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast called outside ToastProvider')
  return ctx
}
