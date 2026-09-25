import { ReactNode, useEffect, useId, useRef } from 'react'
import { X } from 'lucide-react'
import { cn } from './cn'

interface ModalProps {
  title?: ReactNode
  description?: ReactNode
  children: ReactNode
  onClose: () => void
  size?: 'sm' | 'md' | 'lg' | 'xl'
  /** When false, clicking the backdrop does not close the dialog — use for
   *  forms where a stray click would destroy the user's input. ESC and the
   *  close button always work. */
  dismissible?: boolean
}

const sizes = {
  sm: 'max-w-sm',
  md: 'max-w-md',
  lg: 'max-w-2xl',
  xl: 'max-w-4xl',
}

// Modal is the standard dialog primitive. Handles ESC-to-close, scroll
// lock on the document body, and a basic focus trap so keyboard users
// can't tab off the dialog while it's open.
export function Modal({ title, description, children, onClose, size = 'md', dismissible = true }: ModalProps) {
  const ref = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const descriptionId = useId()

  useEffect(() => {
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [])

  // Return focus to whatever opened the dialog — without this, closing
  // drops keyboard users back at the top of the document.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    return () => opener?.focus?.()
  }, [])

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
      if (e.key === 'Tab' && ref.current) {
        const focusable = ref.current.querySelectorAll<HTMLElement>(
          'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])',
        )
        if (focusable.length === 0) return
        const first = focusable[0]
        const last = focusable[focusable.length - 1]
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault()
          last.focus()
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault()
          first.focus()
        }
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  // Move initial focus into the dialog so screen readers announce it.
  useEffect(() => {
    const t = window.setTimeout(() => {
      const first = ref.current?.querySelector<HTMLElement>(
        'input,textarea,select,button',
      )
      first?.focus()
    }, 50)
    return () => window.clearTimeout(t)
  }, [])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? titleId : undefined}
      aria-describedby={description ? descriptionId : undefined}
      className="fixed inset-0 z-50 flex items-end justify-center bg-[rgb(28_34_48/0.32)] p-4 sm:items-center"
      onClick={(e) => {
        if (dismissible && e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={ref}
        className={cn(
          'relative flex max-h-[calc(100dvh-2rem)] w-full flex-col rounded-[12px] border border-border-base bg-bg-surface shadow-[var(--shadow-pop)] animate-slide-up',
          sizes[size],
        )}
      >
        <div className="flex shrink-0 items-start justify-between gap-4 px-5 pt-5">
          <div className="space-y-1">
            {title && <h2 id={titleId} className="text-[16px] font-semibold tracking-[-0.01em] text-fg-base">{title}</h2>}
            {description && <p id={descriptionId} className="text-sm text-fg-muted">{description}</p>}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close dialog"
            className="-mr-2 -mt-2 rounded-md p-1 text-fg-subtle transition-colors hover:bg-bg-raised hover:text-fg-base"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="min-h-0 overflow-y-auto overscroll-contain px-5 pb-5 pt-4">{children}</div>
      </div>
    </div>
  )
}

// ModalFooter is the standard action strip at the bottom of a dialog —
// cancel on the left of the primary action, right-aligned.
export function ModalFooter({ children }: { children: ReactNode }) {
  return <div className="flex justify-end gap-2 pt-4">{children}</div>
}
