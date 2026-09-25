// TrendChart is the console's line chart primitive: dependency-free SVG
// drawn at the container's real pixel width, monotone curves that never
// overshoot the data (a spike can't dip below zero), a faint flat fill
// under the lead series, and a hover crosshair with a value readout.
import { useState } from 'react'
import { useWidth } from '../../lib/useWidth'

export interface TrendSeries {
  label: string
  /** CSS color, typically a --chart-* token. */
  color: string
  values: number[]
  /** Draw only the line, no area fill (defaults to fill on first series). */
  line?: boolean
}

interface TrendChartProps {
  /** X labels, one per slot; first/last render on the axis. */
  labels: string[]
  series: TrendSeries[]
  height?: number
  valueFmt?: (v: number) => string
  emptyText: string
  ariaLabel: string
}

// Fritsch–Carlson monotone cubic: tangents are limited so the curve stays
// within the range of each neighbouring pair of points.
function monotonePath(pts: { x: number; y: number }[]): string {
  const n = pts.length
  if (n === 0) return ''
  if (n === 1) return `M ${pts[0].x} ${pts[0].y}`
  const dx: number[] = []
  const slope: number[] = []
  for (let i = 0; i < n - 1; i++) {
    dx.push(pts[i + 1].x - pts[i].x)
    slope.push((pts[i + 1].y - pts[i].y) / (dx[i] || 1))
  }
  const m: number[] = [slope[0]]
  for (let i = 1; i < n - 1; i++) {
    m.push(slope[i - 1] * slope[i] <= 0 ? 0 : (slope[i - 1] + slope[i]) / 2)
  }
  m.push(slope[n - 2])
  for (let i = 0; i < n - 1; i++) {
    if (slope[i] === 0) { m[i] = 0; m[i + 1] = 0; continue }
    const a = m[i] / slope[i]
    const b = m[i + 1] / slope[i]
    const h = a * a + b * b
    if (h > 9) {
      const t = 3 / Math.sqrt(h)
      m[i] = t * a * slope[i]
      m[i + 1] = t * b * slope[i]
    }
  }
  let d = `M ${pts[0].x.toFixed(1)} ${pts[0].y.toFixed(1)}`
  for (let i = 0; i < n - 1; i++) {
    const c1x = pts[i].x + dx[i] / 3
    const c1y = pts[i].y + (m[i] * dx[i]) / 3
    const c2x = pts[i + 1].x - dx[i] / 3
    const c2y = pts[i + 1].y - (m[i + 1] * dx[i]) / 3
    d += ` C ${c1x.toFixed(1)} ${c1y.toFixed(1)}, ${c2x.toFixed(1)} ${c2y.toFixed(1)}, ${pts[i + 1].x.toFixed(1)} ${pts[i + 1].y.toFixed(1)}`
  }
  return d
}

export function TrendChart({
  labels,
  series,
  height = 160,
  valueFmt = (v) => v.toLocaleString(),
  emptyText,
  ariaLabel,
}: TrendChartProps) {
  const [ref, W] = useWidth<HTMLDivElement>()
  const [hover, setHover] = useState<number | null>(null)
  const AXIS = 48
  const PAD = 8
  const TOP = 10
  const BOTTOM = 24

  const n = labels.length
  const max = Math.max(1, ...series.flatMap((s) => s.values))
  const empty = n === 0 || series.every((s) => s.values.every((v) => v === 0))

  if (empty) {
    return (
      <div className="chart-empty" style={{ height: height + BOTTOM }}>
        {emptyText}
      </div>
    )
  }

  const plotW = W - AXIS - PAD
  const x = (i: number) => (n === 1 ? AXIS + plotW / 2 : AXIS + (i * plotW) / (n - 1))
  const y = (v: number) => TOP + (1 - v / max) * (height - TOP)
  const slot = n > 1 ? plotW / (n - 1) : plotW

  return (
    <div className="trend-chart" style={{ padding: '8px 18px 12px' }}>
     <div ref={ref} style={{ position: 'relative', overflow: 'visible', minWidth: 0 }}>
      <svg width="100%" height={height + BOTTOM} viewBox={`0 0 ${W} ${height + BOTTOM}`} preserveAspectRatio="none" role="img" aria-label={ariaLabel} style={{ display: 'block', overflow: 'visible' }}
        onMouseLeave={() => setHover(null)}>
        {[1, 0.5, 0].map((f) => (
          <g key={f}>
            <line x1={AXIS} x2={W - PAD} y1={y(f * max)} y2={y(f * max)} stroke="var(--chart-grid)" />
            <text x={AXIS - 10} y={y(f * max) + 4} fontSize={11.5} fill="var(--chart-axis)" textAnchor="end" className="tnum">
              {valueFmt(Math.round(f * max))}
            </text>
          </g>
        ))}
        {series.map((s, si) => {
          const pts = s.values.map((v, i) => ({ x: x(i), y: y(v) }))
          const path = monotonePath(pts)
          return (
            <g key={si}>
              {si === 0 && !s.line && pts.length > 1 && (
                <path d={`${path} L ${pts[pts.length - 1].x} ${y(0)} L ${pts[0].x} ${y(0)} Z`} fill={s.color} fillOpacity={0.07} />
              )}
              {pts.length > 1
                ? <path d={path} fill="none" stroke={s.color} strokeWidth={1.75} strokeLinejoin="round" strokeLinecap="round" />
                : <circle cx={pts[0].x} cy={pts[0].y} r={3} fill={s.color} />}
            </g>
          )
        })}
        {hover !== null && (
          <g pointerEvents="none">
            <line x1={x(hover)} x2={x(hover)} y1={TOP} y2={y(0)} stroke="var(--border-strong)" />
            {series.map((s, si) => (
              <circle key={si} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={3.5} fill="var(--bg-surface)" stroke={s.color} strokeWidth={1.75} />
            ))}
          </g>
        )}
        {labels.map((_, i) => (
          <rect key={i} x={x(i) - slot / 2} y={0} width={slot} height={height} fill="transparent" onMouseEnter={() => setHover(i)} />
        ))}
        <text x={AXIS} y={height + 18} fontSize={11.5} fill="var(--chart-axis)">{labels[0]}</text>
        {n > 1 && <text x={W - PAD} y={height + 18} fontSize={11.5} fill="var(--chart-axis)" textAnchor="end">{labels[n - 1]}</text>}
      </svg>
      {hover !== null && (
        <div className="chart-tip" style={{ left: x(hover) > W - 180 ? x(hover) - 172 : x(hover) + 12 }}>
          <div className="chart-tip-label">{labels[hover]}</div>
          {series.map((s) => (
            <div key={s.label} className="chart-tip-row">
              <i style={{ background: s.color }} /> <span>{s.label}</span> <b className="tnum">{valueFmt(s.values[hover] ?? 0)}</b>
            </div>
          ))}
        </div>
      )}
     </div>
    </div>
  )
}
