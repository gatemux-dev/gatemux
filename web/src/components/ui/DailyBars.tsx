// DailyBars is the console's one bar-chart primitive: dependency-free
// inline SVG, one bar per point, tokens for color so both themes work.
import { useWidth } from '../../lib/useWidth'

interface Point {
  label: string
  value: number
  /** Tooltip line; defaults to `label: value`. */
  title?: string
}

interface DailyBarsProps {
  points: Point[]
  /** Bar fill; any CSS color, typically a --chart-* token. */
  color?: string
  height?: number
  valueFmt?: (v: number) => string
  emptyText: string
  ariaLabel: string
}

export function DailyBars({
  points,
  color = 'var(--chart-1)',
  height = 130,
  valueFmt = (v) => v.toLocaleString(),
  emptyText,
  ariaLabel,
}: DailyBarsProps) {
  const [ref, W] = useWidth<HTMLDivElement>()
  const AXIS = 48
  const PAD = 10
  const max = Math.max(1, ...points.map((p) => p.value))

  if (points.length === 0 || points.every((p) => p.value === 0)) {
    return (
      <div className="chart-empty" style={{ height }}>
        {emptyText}
      </div>
    )
  }

  const slot = (W - AXIS - PAD) / points.length
  const barW = Math.max(3, Math.min(28, slot * 0.6))

  return (
    <div style={{ padding: '10px 18px 12px' }}>
     <div ref={ref} style={{ minWidth: 0 }}>
      <svg viewBox={`0 0 ${W} ${height + 22}`} role="img" width={W} height={height + 22} aria-label={ariaLabel} style={{ display: 'block' }}>
        {[1, 0.5].map((f) => (
          <g key={f}>
            <line x1={AXIS} x2={W - PAD} y1={height - f * height + 1} y2={height - f * height + 1} stroke="var(--chart-grid)" />
            <text x={AXIS - 6} y={Math.max(11, height - f * height + 5)} fontSize={11.5} fill="var(--chart-axis)" textAnchor="end">{valueFmt(Math.round(f * max))}</text>
          </g>
        ))}
        <line x1={AXIS} x2={W - PAD} y1={height + 1} y2={height + 1} stroke="var(--chart-grid)" />
        {points.map((p, i) => {
          const h = Math.max(p.value > 0 ? 2 : 0, (p.value / max) * height)
          const x = AXIS + i * slot + (slot - barW) / 2
          return (
            <g key={`${p.label}-${i}`}>
              <title>{p.title ?? `${p.label}: ${valueFmt(p.value)}`}</title>
              <rect x={AXIS + i * slot} y={0} width={slot} height={height} fill="transparent" />
              <rect x={x} y={height - h} width={barW} height={h} rx={2} fill={color} />
            </g>
          )
        })}
        <text x={AXIS} y={height + 16} fontSize={11.5} fill="var(--chart-axis)">{points[0].label}</text>
        <text x={W - PAD} y={height + 16} fontSize={11.5} fill="var(--chart-axis)" textAnchor="end">
          {points[points.length - 1].label}
        </text>
      </svg>
     </div>
    </div>
  )
}
