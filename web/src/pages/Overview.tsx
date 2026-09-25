import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { RefreshCcw } from 'lucide-react'
import { api, type Page } from '../api/client'
import { useQuery } from '../lib/useQuery'
import { fmtUSD } from '../lib/money'
import type { SpendReport, UsageBucket, UsageRow } from '../types'
import {
  Badge,
  Button,
  ErrorState,
  MetricStrip,
  PageHeader,
  RankList,
  SegmentedFilter,
  StatTile,
  TrendChart,
} from '../components/ui'

type Range = '24h' | '7d' | '14d' | '30d'
const RANGES: { value: Range; label: string; hours: number; bucket: 'hour' | 'day' }[] = [
  { value: '24h', label: '24h', hours: 24, bucket: 'hour' },
  { value: '7d', label: '7d', hours: 7 * 24, bucket: 'day' },
  { value: '14d', label: '14d', hours: 14 * 24, bucket: 'day' },
  { value: '30d', label: '30d', hours: 30 * 24, bucket: 'day' },
]

function fmtCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${Math.round(n / 1000)}k`
  if (n >= 1_000) return `${(n / 1000).toFixed(1)}k`
  return n.toLocaleString()
}

export default function Overview() {
  const [range, setRange] = useState<Range>('14d')
  const spec = RANGES.find((r) => r.value === range) ?? RANGES[2]

  const { data, error, loading, refreshing, reload } = useQuery(
    () => {
      const to = new Date()
      const from = new Date(to.getTime() - spec.hours * 3600_000)
      const window = { from: from.toISOString(), to: to.toISOString() }
      return Promise.all([
        api.getInfo(),
        // Charts degrade to empty rather than failing the dashboard.
        api.getUsageAggregate({ ...window, bucket: spec.bucket }).catch(() => [] as UsageBucket[]),
        api.getSpendReport(window).catch(() => null as SpendReport | null),
        api.listProviderHealth().catch(() => null),
        api.listUsage({ status: 'error', from: window.from }, { limit: 5 }).catch(() => null as Page<UsageRow> | null),
      ]).then(([info, buckets, spend, health, errors]) => ({ info, buckets, spend, health, errors }))
    },
    [range],
    { pollMs: 30_000 },
  )

  const info = data?.info ?? null
  const buckets = useMemo(() => data?.buckets ?? [], [data])
  const spend = data?.spend ?? null
  const health = data?.health ?? null
  const recentErrors = data?.errors ?? null

  // The aggregate only returns buckets that saw traffic; zero-fill the window
  // so quiet periods read as quiet instead of vanishing from the axis.
  const slots = useMemo(() => {
    const isHour = spec.bucket === 'hour'
    const count = isHour ? 24 : spec.hours / 24
    const byKey = new Map(buckets.map((b) => [b.bucket.slice(0, isHour ? 13 : 10), b]))
    const out: { key: string; label: string; b: UsageBucket | null }[] = []
    const cursor = new Date()
    if (isHour) {
      cursor.setUTCMinutes(0, 0, 0)
      cursor.setUTCHours(cursor.getUTCHours() - (count - 1))
    } else {
      cursor.setUTCHours(0, 0, 0, 0)
      cursor.setUTCDate(cursor.getUTCDate() - (count - 1))
    }
    for (let i = 0; i < count; i++) {
      const key = cursor.toISOString().slice(0, isHour ? 13 : 10)
      const label = isHour
        ? new Date(`${key}:00:00Z`).toLocaleTimeString(undefined, { hour: 'numeric', timeZone: undefined })
        : new Date(`${key}T00:00:00Z`).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' })
      out.push({ key, label, b: byKey.get(key) ?? null })
      if (isHour) cursor.setUTCHours(cursor.getUTCHours() + 1)
      else cursor.setUTCDate(cursor.getUTCDate() + 1)
    }
    return out
  }, [buckets, spec])

  const totals = useMemo(() => {
    const requests = buckets.reduce((a, b) => a + b.requests, 0)
    const errors = buckets.reduce((a, b) => a + b.errors, 0)
    const tokens = buckets.reduce((a, b) => a + b.total_tokens, 0)
    const costCents = buckets.reduce((a, b) => a + b.cost_cents, 0)
    const cacheHits = buckets.reduce((a, b) => a + (b.cache_hits ?? 0), 0)
    return { requests, errors, tokens, costCents, cacheHits }
  }, [buckets])

  const topAliases = useMemo(
    () => (spend?.aliases ?? []).slice().sort((a, b) => b.requests - a.requests).slice(0, 6),
    [spend],
  )

  const labels = slots.map((s) => s.label)
  const okSeries = slots.map((s) => Math.max(0, (s.b?.requests ?? 0) - (s.b?.errors ?? 0)))
  const errSeries = slots.map((s) => s.b?.errors ?? 0)
  const costSeries = slots.map((s) => s.b?.cost_cents ?? 0)
  const p50Series = slots.map((s) => s.b?.latency_p50_ms ?? 0)
  const p95Series = slots.map((s) => s.b?.latency_p95_ms ?? 0)

  if (error && data === null) {
    return (
      <>
        <PageHeader title="Overview" />
        <ErrorState title="Couldn't load the gateway overview" message={error.message} onRetry={reload} retrying={refreshing} />
      </>
    )
  }

  return (
    <>
      <PageHeader
        title="Overview"
        description="Traffic, spend, and health across the gateway."
        actions={
          <>
            <SegmentedFilter
              variant="window"
              value={range}
              onChange={setRange}
              options={RANGES.map((r) => ({ value: r.value, label: r.label }))}
            />
            <Button variant="ghost" onClick={reload} loading={loading || refreshing} leadingIcon={<RefreshCcw size={14} />}>Refresh</Button>
          </>
        }
      />

      <MetricStrip>
        <StatTile label="Requests" value={loading ? '—' : fmtCount(totals.requests)} hint={`last ${spec.label}`} />
        <StatTile
          label="Error rate"
          value={loading ? '—' : totals.requests === 0 ? '0%' : `${((totals.errors / totals.requests) * 100).toFixed(totals.errors === 0 ? 0 : 1)}%`}
          hint={`${fmtCount(totals.errors)} errors`}
          tone={totals.requests > 0 && totals.errors / totals.requests > 0.05 ? 'warning' : 'neutral'}
        />
        <StatTile label="Tokens" value={loading ? '—' : fmtCount(totals.tokens)} hint={`last ${spec.label}`} />
        <StatTile
          label="Cache hit rate"
          value={loading ? '—' : totals.requests === 0 ? '—' : `${Math.round((totals.cacheHits / totals.requests) * 100)}%`}
          hint={`${fmtCount(totals.cacheHits)} served from cache`}
        />
        <StatTile label="Spend" value={loading ? '—' : fmtUSD(totals.costCents)} hint="estimated" />
      </MetricStrip>

      <div className="chart-grid">
        <section className="data-card" aria-label="Requests">
          <div className="chart-card-h">
            <h2 className="chart-title">Requests</h2>
            <div className="chart-legend">
              <span><i style={{ background: 'var(--chart-1)' }} /> success</span>
              <span><i style={{ background: 'var(--chart-5)' }} /> errors</span>
            </div>
          </div>
          <TrendChart
            labels={labels}
            series={[
              { label: 'success', color: 'var(--chart-1)', values: okSeries },
              { label: 'errors', color: 'var(--chart-5)', values: errSeries, line: true },
            ]}
            valueFmt={fmtCount}
            emptyText={`No traffic in the last ${spec.label}. Send a request through the gateway and it lands here.`}
            ariaLabel="Requests over time"
          />
        </section>

        <section className="data-card" aria-label="Spend">
          <div className="chart-card-h">
            <h2 className="chart-title">Spend</h2>
            <span className="chart-big">estimated, USD</span>
          </div>
          <TrendChart
            labels={labels}
            series={[{ label: 'spend', color: 'var(--chart-1)', values: costSeries }]}
            valueFmt={(v) => fmtUSD(v)}
            emptyText={`No cost recorded in the last ${spec.label}.`}
            ariaLabel="Spend over time"
          />
        </section>

        <section className="data-card" aria-label="Latency">
          <div className="chart-card-h">
            <h2 className="chart-title">Latency</h2>
            <div className="chart-legend">
              <span><i style={{ background: 'var(--chart-1)' }} /> p50</span>
              <span><i style={{ background: 'var(--chart-2)' }} /> p95</span>
            </div>
          </div>
          <TrendChart
            labels={labels}
            series={[
              { label: 'p50', color: 'var(--chart-1)', values: p50Series },
              { label: 'p95', color: 'var(--chart-2)', values: p95Series, line: true },
            ]}
            valueFmt={(v) => `${Math.round(v)}ms`}
            emptyText={`No latency samples in the last ${spec.label}.`}
            ariaLabel="Latency percentiles over time"
          />
        </section>

        <section className="data-card" aria-label="Top models">
          <div className="chart-card-h">
            <h2 className="chart-title">Top models</h2>
            <Link className="linkish" to="/spend">Usage &amp; spend</Link>
          </div>
          <RankList
            empty={`No model traffic in the last ${spec.label}.`}
            rows={topAliases.map((a) => ({
              label: a.alias || '(passthrough)',
              weight: a.requests,
              values: [`${fmtCount(a.requests)} req`, fmtUSD(a.cost_cents)],
            }))}
          />
        </section>
      </div>

      <div className="overview-bottom">
        <section className="card">
          <h2 className="card-title">Recent errors</h2>
          {recentErrors === null && !loading ? (
            <p className="muted">Recent errors are unavailable right now.</p>
          ) : recentErrors && recentErrors.items.length === 0 ? (
            <p className="muted">No failed requests in the last {spec.label}.</p>
          ) : (
            (recentErrors?.items ?? []).map((r) => (
              <Link className="status-line error-line" key={r.id} to={`/usage?request=${r.id}`}>
                <span className="min-w-0">
                  <span className="mono">{r.alias || '(passthrough)'}</span>
                  <span className="muted small error-line-msg">{r.error || r.team_name || r.team_slug}</span>
                </span>
                <span className={`pill pill-${r.status_code >= 500 ? 'err' : 'warn'} tnum`}>{r.status_code}</span>
              </Link>
            ))
          )}
          <Link className="linkish" to="/usage?status=error">All errors in Logs</Link>
        </section>

        <section className="card">
          <h2 className="card-title">Providers</h2>
          {health === null && !loading ? (
            <p className="muted">Provider health is unavailable right now.</p>
          ) : health && health.length === 0 ? (
            <p className="muted">No deployments yet.</p>
          ) : (
            (health ?? []).slice(0, 6).map((p) => (
              <div className="status-line" key={p.name}>
                <span className="mono">{p.name}</span>
                <Badge
                  monospace={false}
                  tone={!p.enabled ? 'neutral' : p.circuit === 'open' ? 'danger' : p.ready ? 'success' : 'warning'}
                >
                  {!p.enabled ? 'Disabled' : p.circuit === 'open' ? 'Circuit open' : p.ready ? 'Healthy' : 'Not ready'}
                </Badge>
              </div>
            ))
          )}
          <Link className="linkish" to="/providers">All provider health</Link>
        </section>

        <section className="card">
          <h2 className="card-title">Gateway</h2>
          <div className="status-line"><Link className="linkish" to="/models">Model aliases</Link><span className="tnum">{info?.aliases.length ?? '—'}</span></div>
          <div className="status-line"><Link className="linkish" to="/models">Deployments</Link><span className="tnum">{info?.deployments.length ?? '—'}</span></div>
          <div className="status-line"><Link className="linkish" to="/teams">Teams</Link><span className="tnum">{info?.counts.teams ?? '—'}</span></div>
          <div className="status-line"><span>Active keys</span><span className="tnum">{info?.counts.active_keys ?? '—'}</span></div>
          <div className="status-line"><span>Database</span><Badge tone={info?.db_ok ? 'success' : 'neutral'} monospace={false}>{!info ? '…' : info.db_ok ? 'Connected' : 'Unavailable'}</Badge></div>
          <div className="status-line"><span title="Streams usage events to logging callbacks. Enabled through the callbacks section of the gateway configuration file — see docs/deploy.md in the repository.">Callback bus</span><Badge tone={info?.redis_ok ? 'success' : 'neutral'} monospace={false}>{!info ? '…' : info.redis_ok ? 'Enabled' : 'Not configured'}</Badge></div>
          <div className="quick-links">
            <Link className="linkish" to="/settings">Settings</Link>
            <Link className="linkish" to="/setup">Setup wizard</Link>
          </div>
        </section>
      </div>
    </>
  )
}
