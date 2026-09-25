// RankList is the "Top models"-style ranked breakdown: a tinted name chip,
// a proportional meter behind the row, and right-aligned values. Rows are
// data, so everything stays tabular and quiet.
import { ReactNode } from 'react'

export interface RankRow {
  label: string
  /** Drives the proportional meter; the largest row fills the track. */
  weight: number
  /** Right-aligned cells, e.g. [requests, cost]. */
  values: ReactNode[]
  href?: string
}

const CHIP_TOKENS = ['--chart-1', '--chart-2', '--chart-3', '--chart-4', '--chart-5']

export function RankList({ rows, empty }: { rows: RankRow[]; empty: string }) {
  if (rows.length === 0) {
    return <div className="chart-empty" style={{ height: 120 }}>{empty}</div>
  }
  const max = Math.max(1, ...rows.map((r) => r.weight))
  return (
    <div className="rank-list">
      {rows.map((row, i) => {
        const token = CHIP_TOKENS[i % CHIP_TOKENS.length]
        return (
          <div className="rank-row" key={row.label}>
            <span className="rank-chip" style={{ color: `var(${token})`, background: `color-mix(in oklab, var(${token}) 12%, transparent)` }}>
              {row.href ? <a href={row.href}>{row.label}</a> : row.label}
            </span>
            <span className="rank-meter" aria-hidden>
              <span className="rank-meter-fill" style={{ width: `${(row.weight / max) * 100}%`, background: `var(${token})` }} />
            </span>
            {row.values.map((v, j) => (
              <span className="rank-value tnum" key={j}>{v}</span>
            ))}
          </div>
        )
      })}
    </div>
  )
}
