import { ReactNode } from 'react'
import { cn } from './cn'

interface TableProps {
  head: Array<ReactNode>
  children: ReactNode
  /** When true, wraps the table in the handoff's `.data-card` shell. */
  bordered?: boolean
  /** Optional header strip — replaces the default header row. */
  header?: ReactNode
  /** Optional footer strip — pagination, summary etc. */
  footer?: ReactNode
  className?: string
}

// Maps to the handoff `.data-card` + `.data-table` markup so every table
// shares the same row min-height, divider, hover tint, and sticky header.
export function Table({ head, children, bordered = true, header, footer, className }: TableProps) {
  return (
    <div className={cn(bordered && 'data-card', className)}>
      {header}
      <div className="data-scroll">
        <table className="data-table">
          <thead>
            <tr>
              {head.map((h, i) => (
                <th key={i}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>{children}</tbody>
        </table>
      </div>
      {footer}
    </div>
  )
}

export function Tr({
  children,
  className,
  onClick,
  expanded,
  error,
}: {
  children: ReactNode
  className?: string
  onClick?: () => void
  expanded?: boolean
  error?: boolean
}) {
  // A clickable row must also be keyboard-operable: focusable and activated
  // with Enter/Space. It keeps its row role so the table stays navigable.
  const interactive = onClick
    ? {
        onClick,
        tabIndex: 0,
        onKeyDown: (e: React.KeyboardEvent<HTMLTableRowElement>) => {
          if (e.target !== e.currentTarget) return
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onClick()
          }
        },
      }
    : {}
  return (
    <tr
      {...interactive}
      className={cn(
        'data-row',
        expanded && 'is-expanded',
        error && 'is-err',
        className,
      )}
    >
      {children}
    </tr>
  )
}

export function Td({
  children,
  className,
  mono,
  num,
  align,
}: {
  children: ReactNode
  className?: string
  mono?: boolean
  num?: boolean
  align?: 'left' | 'right' | 'center'
}) {
  return (
    <td
      className={cn(
        num && 'cell-num tnum',
        align === 'right' && 'text-right',
        align === 'center' && 'text-center',
        mono && 'mono',
        className,
      )}
    >
      {children}
    </td>
  )
}

export function EmptyRow({ cols, children }: { cols: number; children: ReactNode }) {
  return (
    <tr>
      <td colSpan={cols}>
        <div className="rows-empty">{children}</div>
      </td>
    </tr>
  )
}
