import { ReactNode, useEffect, useRef } from 'react'
import { X } from 'lucide-react'

// Drawer is a right-hand side panel for inspecting one record while the
// list stays in view. Escape and the scrim close it; focus moves into the
// panel on open and back to the opener on close.
export function Drawer({
  title,
  subtitle,
  actions,
  onClose,
  children,
  width = 720,
}: {
  title: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  onClose: () => void
  children: ReactNode
  width?: number
}) {
  const panel = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    panel.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      opener?.focus?.()
    }
  }, [onClose])

  return (
    <div className="drawer-root" role="presentation">
      <div className="drawer-scrim" onClick={onClose} />
      <div
        ref={panel}
        className="drawer-panel"
        role="dialog"
        aria-modal="true"
        aria-label={typeof title === 'string' ? title : 'Details'}
        tabIndex={-1}
        style={{ width: `min(${width}px, 100vw)` }}
      >
        <header className="drawer-head">
          <div className="min-w-0">
            <div className="drawer-title">{title}</div>
            {subtitle && <div className="drawer-sub">{subtitle}</div>}
          </div>
          <div className="drawer-actions">
            {actions}
            <button type="button" className="drawer-close" onClick={onClose} aria-label="Close">
              <X size={16} />
            </button>
          </div>
        </header>
        <div className="drawer-body">{children}</div>
      </div>
    </div>
  )
}
