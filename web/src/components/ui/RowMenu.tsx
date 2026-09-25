import { ReactNode, useEffect, useRef, useState } from 'react'
import { MoreHorizontal } from 'lucide-react'

export interface RowMenuItem {
  label: ReactNode
  onSelect: () => void
  /** Renders in the danger color and separated from routine actions. */
  destructive?: boolean
  disabled?: boolean
}

// RowMenu collapses a table row's action links into one ⋯ menu so
// destructive actions stop sitting one accidental click away from routine
// ones. Dependency-free: portal-less popover, closed on outside click and
// Escape.
export function RowMenu({ label, items, trigger }: {
  label: string
  items: RowMenuItem[]
  /** Visible button content; omitted, the menu shows a quiet ⋯ icon. */
  trigger?: ReactNode
}) {
  // Anchor rect for the open popover. Fixed positioning (viewport-relative)
  // lets the menu escape the table wrapper's overflow clipping.
  const [anchor, setAnchor] = useState<{ top: number; right: number } | null>(null)
  const open = anchor !== null
  const ref = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (!open) return
    const close = () => setAnchor(null)
    const onDocClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) close()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [open])

  const routine = items.filter((i) => !i.destructive)
  const destructive = items.filter((i) => i.destructive)

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-block' }}>
      <button
        type="button"
        className={trigger ? 'btn btn-ghost menu-trigger' : 'btn btn-ghost'}
        style={trigger ? undefined : { padding: '2px 8px' }}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        onClick={(e) => {
          e.stopPropagation()
          if (anchor) {
            setAnchor(null)
          } else {
            const rect = e.currentTarget.getBoundingClientRect()
            setAnchor({ top: rect.bottom + 4, right: window.innerWidth - rect.right })
          }
        }}
      >
        {trigger ?? <MoreHorizontal size={14} />}
      </button>
      {open && (
        <div
          role="menu"
          aria-label={label}
          className="card"
          style={{
            position: 'fixed',
            right: anchor.right,
            top: anchor.top,
            zIndex: 40,
            minWidth: 180,
            padding: 4,
            display: 'grid',
            gap: 0,
            boxShadow: 'var(--shadow-2, 0 6px 24px rgba(0,0,0,.12))',
          }}
        >
          {routine.map((item, i) => (
            <MenuButton key={i} item={item} close={() => setAnchor(null)} />
          ))}
          {routine.length > 0 && destructive.length > 0 && (
            <div style={{ borderTop: '1px solid var(--border, rgba(0,0,0,.08))', margin: '4px 0' }} />
          )}
          {destructive.map((item, i) => (
            <MenuButton key={`d${i}`} item={item} close={() => setAnchor(null)} />
          ))}
        </div>
      )}
    </div>
  )
}

function MenuButton({ item, close }: { item: RowMenuItem; close: () => void }) {
  return (
    <button
      type="button"
      role="menuitem"
      disabled={item.disabled}
      className={'menu-item' + (item.destructive ? ' is-danger' : '')}
      onClick={(e) => {
        e.stopPropagation()
        close()
        item.onSelect()
      }}
    >
      {item.label}
    </button>
  )
}
