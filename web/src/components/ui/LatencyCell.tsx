import { cn } from './cn'

interface LatencyCellProps {
  ms: number
  /** Optional p50 for tone scaling. Default thresholds: <500=ok, <2000=warn, else err. */
  warnAtMs?: number
  errAtMs?: number
}

// LatencyCell is the bar+ms cell from the handoff. The bar fills relative
// to a reference (200ms = full bar by default) and the bar color +
// number color shift with the configured warn / err thresholds.
export function LatencyCell({ ms, warnAtMs = 500, errAtMs = 2000 }: LatencyCellProps) {
  const tone = ms >= errAtMs ? 'lat-err' : ms >= warnAtMs ? 'lat-warn' : 'lat-ok'
  // Visual cap at 2.5s so a slow request still shows a tail of bar.
  const fraction = Math.min(1, ms / 2500)
  return (
    <span className={cn('lat-cell', tone)}>
      <span className="lat-bar">
        <span className="lat-fill" style={{ width: `${Math.max(6, fraction * 100)}%` }} />
      </span>
      <span className="tnum">{ms < 1000 ? ms : (ms / 1000).toFixed(2)}</span>
      <span className="lat-unit">{ms < 1000 ? 'ms' : 's'}</span>
    </span>
  )
}
