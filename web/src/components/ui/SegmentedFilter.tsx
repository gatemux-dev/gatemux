import { ReactNode } from 'react'
import { cn } from './cn'

export interface SegmentOption<V extends string> {
  value: V
  label: ReactNode
  count?: number
  tone?: 'ok' | 'warn' | 'err'
}

interface SegmentedFilterProps<V extends string> {
  value: V
  options: SegmentOption<V>[]
  onChange: (v: V) => void
  /** Use 'window' rendering when the segmented filter holds time windows
   *  (24h / 7d / 30d) — has slightly different toggle styling. */
  variant?: 'status' | 'window'
  className?: string
}

// SegmentedFilter is the counted-segment toggle from the handoff. Each
// option carries its own tone so the active "Errors (12)" segment can
// stay danger-tinted while "All (1.2k)" stays neutral.
export function SegmentedFilter<V extends string>({
  value,
  options,
  onChange,
  variant = 'status',
  className,
}: SegmentedFilterProps<V>) {
  const wrapperClass = variant === 'window' ? 'seg-window' : 'seg-status'
  return (
    <div className={cn(wrapperClass, className)} role="group">
      {options.map((opt) => {
        const toneClass =
          opt.tone === 'ok' ? 'seg-ok' : opt.tone === 'warn' ? 'seg-warn' : opt.tone === 'err' ? 'seg-err' : ''
        return (
          <button
            key={opt.value}
            type="button"
            aria-pressed={value === opt.value}
            className={cn('seg-btn', toneClass, value === opt.value && 'is-on')}
            onClick={() => onChange(opt.value)}
          >
            <span>{opt.label}</span>
            {opt.count != null && <span className="seg-count">{opt.count.toLocaleString()}</span>}
          </button>
        )
      })}
    </div>
  )
}
