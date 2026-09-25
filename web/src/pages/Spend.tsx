import { useEffect, useMemo, useRef, useState } from 'react'
import { RefreshCcw } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import { useQuery } from '../lib/useQuery'
import { fmtUSD } from '../lib/money'
import { principalIsAdmin, principalTeamSlug, type Principal } from '../auth'
import type { SpendProjection, SpendReport, SpendTimeseries, Team, TeamMember } from '../types'
import {
  Badge,
  Button,
  DailyBars,
  EmptyRow,
  Input,
  MetricStrip,
  PageHeader,
  SegmentedFilter,
  StatTile,
  Table,
  Td,
  Tr,
  useToast,
} from '../components/ui'

export default function Spend({ principal }: { principal: Principal }) {
  const requestVersion = useRef(0)
  useEffect(() => () => { requestVersion.current++ }, [])
  const isAdmin = principalIsAdmin(principal)
  const ownTeam = principalTeamSlug(principal)
  const [teams, setTeams] = useState<Team[]>([])
  const [users, setUsers] = useState<TeamMember[]>([])
  const [usersTotal, setUsersTotal] = useState(0)
  const [spend, setSpend] = useState<SpendReport | null>(null)
  const [series, setSeries] = useState<SpendTimeseries | null>(null)
  const [filters, setFilters] = useState({ team: isAdmin ? '' : ownTeam ?? '', user_id: '', from: '', to: '' })
  const [preset, setPreset] = useState<'7d' | '30d' | 'mtd' | 'custom'>('30d')
  const [groupBy, setGroupBy] = useState<GroupBy>('alias')
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const toast = useToast()

  // Accepts the filters to load so a preset click can apply and reload in one
  // step without racing React state.
  const reload = async (next?: typeof filters, nextGroup: GroupBy = groupBy) => {
    const active = next ?? filters
    const version = ++requestVersion.current
    setLoading(true)
    try {
      if (!isAdmin && !ownTeam) throw new Error('No team is assigned to this account.')
      // The date inputs produce YYYY-MM-DD, but the spend endpoints only
      // accept RFC3339 — without this conversion any date filter 400s.
      const scope = {
        team: active.team || undefined,
        user_id: active.user_id || undefined,
        from: active.from ? `${active.from}T00:00:00Z` : undefined,
        to: active.to ? `${active.to}T23:59:59Z` : undefined,
      }
      const [t, u, s, ts] = await Promise.all([
        api.listTeams({ limit: 200 }),
        isAdmin ? api.listUsers({ limit: 500 }) : api.listTeamMembers(ownTeam!, { limit: 500 }),
        api.getSpendReport({ ...scope, group_by: nextGroup === 'alias' ? undefined : nextGroup }),
        // Charts degrade to empty rather than failing the page.
        api.getSpendTimeseries({ ...scope, bucket: 'day' }).catch(() => null),
      ])
      if (version !== requestVersion.current) return
      setTeams(t.items)
      setUsers(u.items)
      setUsersTotal(u.total)
      setSpend(s)
      setSeries(ts)
      setLoadError(null)
    } catch (e) {
      if (version !== requestVersion.current) return
      setLoadError(e instanceof Error ? e.message : 'Could not load spend data.')
      setSpend(null)
      toast.error('Failed to load spend', e instanceof ApiError ? e.message : undefined)
    } finally {
      if (version === requestVersion.current) setLoading(false)
    }
  }

  // Presets write concrete dates into the filters and reload in one step;
  // 'custom' only reveals the date inputs and waits for Apply.
  const applyPreset = (value: '7d' | '30d' | 'mtd' | 'custom') => {
    setPreset(value)
    if (value === 'custom') return
    const now = new Date()
    const from = value === 'mtd'
      ? new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1))
      : new Date(now.getTime() - (value === '7d' ? 7 : 30) * 24 * 3600_000)
    const next = { ...filters, from: from.toISOString().slice(0, 10), to: now.toISOString().slice(0, 10) }
    setFilters(next)
    void reload(next)
  }

  useEffect(() => {
    reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Memoized so ForecastsSection doesn't receive a fresh array identity on
  // every parent render (each keystroke in the date inputs used to refire
  // one projection request per team).
  const budgetedTeams = useMemo(
    () => teams.filter((t) => t.usd_limit_cents != null && t.usd_limit_cents > 0),
    [teams],
  )

  const totalCost = spend?.total.cost_cents ?? 0
  const totalRequests = spend?.total.requests ?? 0
  const totalPrompt = spend?.total.prompt_tokens ?? 0
  const totalCompletion = spend?.total.completion_tokens ?? 0

  if (loadError) return <section className="card" role="alert"><h1>Spend unavailable</h1><p>{loadError}</p><Button onClick={() => reload()} loading={loading}>Retry</Button></section>

  return (
    <>
      <PageHeader
        title="Spend & usage"
        description={isAdmin ? 'Spend, pricing, and forecasts across teams.' : 'Spend for your team only.'}
        actions={
          <Button variant="ghost" leadingIcon={<RefreshCcw size={13} />} onClick={() => reload()} loading={loading}>
            Refresh
          </Button>
        }
      />

      <div className="page-toolbar spend-toolbar">
        <SegmentedFilter
          variant="window"
          value={preset}
          onChange={(v) => applyPreset(v)}
          options={[
            { value: '7d', label: '7d' },
            { value: '30d', label: '30d' },
            { value: 'mtd', label: 'Month to date' },
            { value: 'custom', label: 'Custom' },
          ]}
        />
        {preset === 'custom' && (
          <div className="spend-dates">
            <Input aria-label="From date" type="date" value={filters.from} onChange={(e) => setFilters((f) => ({ ...f, from: e.target.value }))} className="!w-auto" />
            <span className="muted">to</span>
            <Input aria-label="To date" type="date" value={filters.to} onChange={(e) => setFilters((f) => ({ ...f, to: e.target.value }))} className="!w-auto" />
            <Button variant="ghost" onClick={() => reload()} loading={loading}>Apply</Button>
          </div>
        )}
        <div className="spend-scope">
          <label className="select-shell">
            <select aria-label="Team filter" disabled={!isAdmin} value={filters.team} onChange={(e) => { const next = { ...filters, team: e.target.value, user_id: '' }; setFilters(next); void reload(next) }}>
              {isAdmin && <option value="">All teams</option>}
              {teams.map((t) => (
                <option key={t.slug} value={t.slug}>{t.name}</option>
              ))}
            </select>
          </label>
          <label className="select-shell">
            <select aria-label="User filter" value={filters.user_id} onChange={(e) => { const next = { ...filters, user_id: e.target.value }; setFilters(next); void reload(next) }}>
              <option value="">All users</option>
              {users.map((u) => (
                <option key={u.id} value={String(u.id)}>{u.email}</option>
              ))}
            </select>
          </label>
        </div>
      </div>
      {usersTotal > users.length && <p className="muted">User filter lists the first {users.length} of {usersTotal} directory members. All-user totals include everyone in scope.</p>}

      <MetricStrip>
        <StatTile label="Requests" value={spend ? totalRequests.toLocaleString() : '—'} hint={windowLabel(spend)} />
        <StatTile
          label="Tokens"
          value={spend ? (totalPrompt + totalCompletion).toLocaleString() : '—'}
          hint={spend ? `${totalPrompt.toLocaleString()} in, ${totalCompletion.toLocaleString()} out` : undefined}
        />
        <StatTile
          label="Estimated cost"
          value={spend ? fmtUSD(totalCost) : '—'}
          hint="Estimate, not a provider invoice"
          tone="accent"
        />
      </MetricStrip>
      <p className="muted small">
        Recorded totals include conservative estimates and may exclude unknown or unpriced requests; they are not provider invoices.
        Each request’s accounting evidence is in <Link to="/usage">Logs</Link>.
      </p>

      <div className="chart-row" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))' }}>
        <section className="data-card" aria-label="Spend per day">
          <div className="data-head"><h2 className="data-h">Spend per day</h2></div>
          <DailyBars
            points={(series?.series ?? []).map((b) => ({
              label: shortDay(b.bucket),
              value: b.total.cost_cents,
              title: `${shortDay(b.bucket)}: ${fmtUSD(b.total.cost_cents)} · ${b.total.requests.toLocaleString()} requests`,
            }))}
            valueFmt={(v) => fmtUSD(v)}
            emptyText="No recorded spend in this window."
            ariaLabel="Daily spend"
          />
        </section>
        <section className="data-card" aria-label="Requests per day">
          <div className="data-head"><h2 className="data-h">Requests per day</h2></div>
          <DailyBars
            points={(series?.series ?? []).map((b) => ({
              label: shortDay(b.bucket),
              value: b.total.requests,
            }))}
            color="var(--chart-2)"
            emptyText="No requests in this window."
            ariaLabel="Daily requests"
          />
        </section>
      </div>

      <BreakdownTable
        spend={spend}
        groupBy={groupBy}
        options={GROUPS.filter((g) => isAdmin || g.value !== 'team')}
        onGroupBy={(g) => { setGroupBy(g); void reload(undefined, g) }}
      />

      {isAdmin && <ForecastsSection teams={budgetedTeams} />}

      {isAdmin && (
        <p className="muted small">Model prices are set under <Link to="/models?tab=pricing">Models → Pricing</Link>.</p>
      )}
    </>
  )
}

type GroupBy = 'alias' | 'team' | 'key' | 'user' | 'customer'

const GROUPS: { value: GroupBy; label: string; column: string }[] = [
  { value: 'alias', label: 'Model', column: 'Model alias' },
  { value: 'team', label: 'Team', column: 'Team' },
  { value: 'key', label: 'Key', column: 'Key' },
  { value: 'user', label: 'User', column: 'User' },
  { value: 'customer', label: 'Customer', column: 'Customer' },
]

// BreakdownTable answers "who or what spent it": one row per model alias,
// team, key, user or customer, with each row's share of the total cost.
function BreakdownTable({ spend, groupBy, options, onGroupBy }: {
  spend: SpendReport | null
  groupBy: GroupBy
  options: typeof GROUPS
  onGroupBy: (g: GroupBy) => void
}) {
  const rows = !spend
    ? null
    : groupBy === 'alias'
      ? spend.aliases.map((a) => ({ key: a.alias, label: '', ...a }))
      : spend.group_by === groupBy ? (spend.breakdown ?? []).map((g) => ({ ...g, label: g.label ?? '' })) : null
  const total = spend?.total.cost_cents ?? 0
  const column = GROUPS.find((g) => g.value === groupBy)!.column
  return (
    <Table
      head={[
        column,
        <span className="cell-num" key="r">Requests</span>,
        <span className="cell-num" key="t">Tokens</span>,
        <span className="cell-num" key="$">Cost</span>,
        <span key="s">Share of cost</span>,
      ]}
      header={
        <div className="data-head">
          <h2 className="data-h">
            Breakdown {rows && <span className="data-count tnum">{rows.length}</span>}
          </h2>
          <SegmentedFilter
            variant="window"
            value={groupBy}
            onChange={onGroupBy}
            options={options.map((g) => ({ value: g.value, label: g.label }))}
          />
        </div>
      }
    >
      {rows === null ? (
        <SkeletonRows cols={5} />
      ) : rows.length === 0 ? (
        <EmptyRow cols={5}>No spend in the selected window.</EmptyRow>
      ) : (
        rows.map((r) => {
          const share = total > 0 ? r.cost_cents / total : 0
          return (
            <Tr key={r.key}>
              <Td>
                {groupBy === 'team' ? (
                  <><span>{r.label || r.key}</span> <span className="mono muted small">{r.key}</span></>
                ) : (
                  <><span className="mono">{r.key}</span>{r.label && <span className="muted"> {r.label}</span>}</>
                )}
              </Td>
              <Td num mono>{r.requests.toLocaleString()}</Td>
              <Td num mono>{(r.prompt_tokens + r.completion_tokens).toLocaleString()}</Td>
              <Td num mono>{fmtUSD(r.cost_cents)}</Td>
              <Td>
                <span className="share" title={`${(share * 100).toFixed(1)}% of cost`}>
                  <span className="share-bar"><span style={{ width: `${Math.max(share * 100, share > 0 ? 1 : 0)}%` }} /></span>
                  <span className="tnum muted">{(share * 100).toFixed(share > 0 && share < 0.1 ? 1 : 0)}%</span>
                </span>
              </Td>
            </Tr>
          )
        })
      )}
    </Table>
  )
}

function shortDay(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function windowLabel(spend: SpendReport | null): string | undefined {
  if (!spend) return undefined
  return `${shortDay(spend.from)} – ${shortDay(spend.to)}`
}

type ForecastRow = { team: Team; projection: SpendProjection | null; error?: string }

function ForecastsSection({ teams }: { teams: Team[] }) {
  // Key the fetch on the set of budgeted team ids, not the array identity —
  // projections refetch only when that set actually changes. Per-team
  // failures degrade to an error card rather than failing the batch.
  const teamsKey = teams.map((t) => t.id).join(',')
  const { data } = useQuery(
    () =>
      Promise.all(
        teams.map(async (team): Promise<ForecastRow> => {
          try {
            const projection = await api.getProjection('team', team.id)
            return { team, projection }
          } catch (e) {
            return { team, projection: null, error: e instanceof ApiError ? e.message : 'failed' }
          }
        }),
      ),
    [teamsKey],
    { enabled: teams.length > 0 },
  )
  const [showAll, setShowAll] = useState(false)
  // Most at risk first: projected spend as a share of the limit, errors last.
  const risk = (r: ForecastRow) => r.projection && r.team.usd_limit_cents ? r.projection.projected_cents / r.team.usd_limit_cents : -1
  const sorted = data ? [...data].sort((a, b) => risk(b) - risk(a)) : null
  const rows = sorted && !showAll ? sorted.slice(0, 6) : sorted

  if (teams.length === 0) return null

  return (
    <section className="flex flex-col gap-3">
      <div className="data-head">
        <h2 className="data-h">
          Forecasts <span className="data-count tnum">{teams.length}</span>
        </h2>
        <span className="muted">Projected end-of-period spend, teams closest to their budget first</span>
      </div>
      {rows === null ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {teams.slice(0, 6).map((t) => (
            <div key={t.slug} className="data-card" style={{ padding: 16 }}>
              <span className="skel" style={{ width: 120, display: 'block', marginBottom: 8 }} />
              <span className="skel" style={{ width: 180, display: 'block' }} />
            </div>
          ))}
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {rows.map((row) => (
            <ForecastCard key={row.team.slug} row={row} />
          ))}
        </div>
      )}
      {sorted && sorted.length > 6 && (
        <button type="button" className="linkish" style={{ alignSelf: 'flex-start' }} onClick={() => setShowAll((v) => !v)}>
          {showAll ? 'Show the six closest to budget' : `Show all ${sorted.length} teams`}
        </button>
      )}
    </section>
  )
}

function ForecastCard({ row }: { row: ForecastRow }) {
  const { team, projection, error } = row
  const limit = team.usd_limit_cents ?? 0

  if (error || !projection) {
    return (
      <div className="data-card" style={{ padding: 16 }}>
        <div className="flex items-center justify-between">
          <span className="data-h" style={{ fontSize: 14 }}>{team.name}</span>
          <Badge tone="danger">error</Badge>
        </div>
        <div className="muted" style={{ marginTop: 8 }}>{error ?? 'No projection available'}</div>
      </div>
    )
  }

  const tone =
    projection.on_track === 'above'
      ? 'danger'
      : projection.on_track === 'on'
        ? 'warning'
        : projection.on_track === 'below'
          ? 'success'
          : 'neutral'
  const label =
    projection.on_track === 'above'
      ? 'over pace'
      : projection.on_track === 'on'
        ? 'near limit'
        : projection.on_track === 'below'
          ? 'on track'
          : 'pending'
  const spend = fmtUSD(projection.spend_so_far_cents)
  const limitDollars = fmtUSD(limit)
  const projected = fmtUSD(projection.projected_cents)
  const pctUsed = limit > 0 ? Math.min(100, (projection.spend_so_far_cents / limit) * 100) : 0
  const pctProjected = limit > 0 ? Math.min(120, (projection.projected_cents / limit) * 100) : 0
  const days = projection.days_to_limit
  // A six-digit day count is noise, not a forecast; past a year we just say so.
  const daysCopy =
    days != null && days > 0 && Number.isFinite(days)
      ? days < 1
        ? 'less than a day to limit'
        : days > 365
          ? 'over a year of headroom at this pace'
          : `~${Math.round(days)} day${Math.round(days) === 1 ? '' : 's'} to limit`
      : null

  return (
    <div className="data-card" style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div className="flex items-center justify-between">
        <span className="min-w-0">
          <span className="data-h block truncate" style={{ fontSize: 14 }}>{team.name}</span>
          {team.name !== team.slug && <span className="mono block truncate text-[11px] text-fg-subtle">{team.slug}</span>}
        </span>
        <Badge tone={tone}>{label}</Badge>
      </div>

      {projection.needs_more_data ? (
        <div className="muted">Not enough data this period to project yet.</div>
      ) : (
        <>
          <div className="flex items-baseline justify-between">
            <span className="muted">Spent so far</span>
            <span className="mono tnum">{spend} / {limitDollars}</span>
          </div>
          <div
            style={{
              position: 'relative',
              height: 6,
              borderRadius: 3,
              background: 'var(--surface-2, rgba(127,127,127,0.15))',
              overflow: 'hidden',
            }}
          >
            <div
              style={{
                width: `${pctProjected}%`,
                height: '100%',
                background: tone === 'danger' ? 'var(--danger)' : tone === 'warning' ? 'var(--warning)' : 'var(--success)',
                opacity: 0.35,
              }}
            />
            <div
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: `${pctUsed}%`,
                height: '100%',
                background: tone === 'danger' ? 'var(--danger)' : tone === 'warning' ? 'var(--warning)' : 'var(--success)',
              }}
            />
          </div>
          <div className="flex items-baseline justify-between">
            <span className="muted">Projected end-of-period</span>
            <span className="mono tnum">{projected}</span>
          </div>
          {daysCopy && <div className="muted" style={{ fontSize: 12 }}>{daysCopy}</div>}
        </>
      )}
    </div>
  )
}

function SkeletonRows({ cols }: { cols: number }) {
  return (
    <>
      {Array.from({ length: 5 }).map((_, i) => (
        <tr key={i} className="data-row">
          {Array.from({ length: cols }).map((__, j) => (
            <td key={j}><span className="skel" style={{ width: 80, display: 'block' }} /></td>
          ))}
        </tr>
      ))}
    </>
  )
}
