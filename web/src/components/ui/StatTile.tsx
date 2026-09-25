import { ReactNode } from 'react'
import { cn } from './cn'

interface StatTileProps {
  label: ReactNode
  value: ReactNode
  hint?: ReactNode
  tone?: 'neutral' | 'accent' | 'success' | 'warning' | 'danger'
  className?: string
}

// StatTile uses the handoff's `.stat-tile` class. Render a row of these
// inside a `.stat-row` parent (or wrap them with <MetricStrip>).
const toneClass = {
  neutral: '',
  accent:  'tone-accent',
  success: 'tone-ok',
  warning: 'tone-warn',
  danger:  'tone-err',
}

export function StatTile({ label, value, hint, tone = 'neutral', className }: StatTileProps) {
  // A word like "unlimited" is a state, not a figure — render it at text
  // size instead of as a giant lowercase stat value.
  const isText = typeof value === 'string' && !/\d/.test(value)
  return (
    <div className={cn('stat-tile', toneClass[tone], className)}>
      <div className="stat-label">{label}</div>
      <div className={cn('stat-value', isText && 'is-text')}>{value}</div>
      {hint && <div className="stat-sub">{hint}</div>}
    </div>
  )
}

// MetricStrip is the connected 7-cell KPI bar described in the handoff.
// Children should be StatTile elements; the parent gives them a single
// rounded surface with vertical dividers between cells.
export function MetricStrip({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('stat-row', className)}>{children}</div>
}
