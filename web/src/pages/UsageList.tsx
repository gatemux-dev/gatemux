import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import {
  Activity,
  AlertTriangle,
  Bookmark,
  Copy,
  Download,
  Inbox,
  Link2,
  MessageSquare,
  Pause,
  Play,
  Radio,
  RefreshCcw,
  Search,
  SlidersHorizontal,
  Tag,
  Users as UsersIcon,
  X,
  Zap,
} from 'lucide-react'
import { api, ApiError, type UsageQuery } from '../api/client'
import { principalIsAdmin, type Principal } from '../auth'
import type { ReplayResponse, Team, UsageBucket, UsageFacets, UsagePayload, UsageRow } from '../types'
import { useQuery } from '../lib/useQuery'
import { useUrlState } from '../lib/useUrlState'
import {
  Badge,
  Button,
  Drawer,
  EmptyRow,
  ErrorRow,
  MetricStrip,
  PageHeader,
  RowMenu,
  SegmentedFilter,
  StatTile,
  Table,
  Td,
  Tr,
  useToast,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

type StatusFilter = 'all' | 'error' | '2xx' | '4xx' | '5xx'
type LatencyFilter = '' | 'fast' | 'med' | 'slow'
type WindowChoice = '1h' | '24h' | '7d' | '30d'

const WINDOW_OPTIONS: { value: WindowChoice; label: string; hours: number; bucket: 'hour' | 'day' }[] = [
  { value: '1h',  label: '1h',  hours: 1,       bucket: 'hour' },
  { value: '24h', label: '24h', hours: 24,      bucket: 'hour' },
  { value: '7d',  label: '7d',  hours: 24 * 7,  bucket: 'hour' },
  { value: '30d', label: '30d', hours: 24 * 30, bucket: 'day' },
]

const FILTER_DEFAULTS = {
  team: '', alias: '', key: '', status: 'all', latency: '', q: '', window: '24h', request: '',
}
const FILTER_KEYS = ['team', 'alias', 'key', 'status', 'latency', 'q'] as const

// Built-in views are shortcuts to common filter sets; saved views are the
// viewer's own and live in this browser only.
type View = { name: string; query: string }
const BUILTIN_VIEWS: View[] = [
  { name: 'Server errors', query: 'status=5xx' },
  { name: 'Client errors', query: 'status=4xx' },
  { name: 'Slow requests', query: 'latency=slow' },
]
const VIEWS_KEY = 'gatemux.logs.views'

function loadViews(): View[] {
  try {
    const raw = localStorage.getItem(VIEWS_KEY)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((v) => typeof v?.name === 'string' && typeof v?.query === 'string') : []
  } catch {
    return []
  }
}

function saveViews(views: View[]) {
  try { localStorage.setItem(VIEWS_KEY, JSON.stringify(views)) } catch { /* storage unavailable */ }
}

export default function UsageList({ principal }: { principal: Principal }) {
  const isAdmin = principalIsAdmin(principal)
  const toast = useToast()
  const [f, setF] = useUrlState(FILTER_DEFAULTS)
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)
  const [teams, setTeams] = useState<Team[]>([])
  const [aliasCatalog, setAliasCatalog] = useState<string[]>([])
  const [live, setLive] = useState(false)
  const [search, setSearch] = useState(f.q)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [views, setViews] = useState<View[]>(loadViews)

  const windowSpec = WINDOW_OPTIONS.find((w) => w.value === f.window) ?? WINDOW_OPTIONS[1]

  useEffect(() => {
    if (!isAdmin) return
    api.listTeams({ limit: 500 }).then((p) => setTeams(p.items)).catch(() => {})
    api.listAliases({ limit: 500 }).then((p) => setAliasCatalog(p.items.map((a) => a.alias).sort())).catch(() => {})
  }, [isAdmin])

  // The search box writes to the URL after a pause so typing doesn't
  // refetch on every keystroke; the URL stays the source of truth.
  useEffect(() => setSearch(f.q), [f.q])
  useEffect(() => {
    const t = window.setTimeout(() => { if (search !== f.q) setF({ q: search }) }, 300)
    return () => window.clearTimeout(t)
  }, [search, f.q, setF])

  const filterKey = FILTER_KEYS.map((k) => f[k]).join('|') + '|' + f.window
  useEffect(() => { setOffset(0) }, [filterKey])

  const { data, error, refreshing, reload } = useQuery(
    () => {
      const to = new Date()
      const from = new Date(to.getTime() - windowSpec.hours * 3600_000)
      const query: UsageQuery = {
        team: f.team || undefined, from: from.toISOString(), alias: f.alias, key: f.key,
        status: f.status, latency: f.latency, q: f.q,
      }
      return Promise.all([
        api.listUsage(query, { limit, offset }),
        api.getUsageFacets(query).catch(() => null as UsageFacets | null),
        // Tiles describe all traffic in the window (team-scoped); a failed
        // aggregate degrades to dashes rather than failing the page.
        api.getUsageAggregate({ team: f.team || undefined, from: from.toISOString(), to: to.toISOString(), bucket: windowSpec.bucket })
          .catch(() => [] as UsageBucket[]),
      ]).then(([usage, facets, agg]) => ({ usage, facets, aggregate: agg ?? [] }))
    },
    [filterKey, limit, offset],
    // Live tail: poll every 2s while the toggle is on. The hook skips
    // ticks while the tab is hidden or a request is still in flight.
    { pollMs: live ? 2000 : undefined },
  )
  const rows = data?.usage.items ?? null
  const total = data?.usage.total ?? 0
  const facets = data?.facets ?? null
  const aggregate = data?.aggregate ?? null

  // Window-accurate totals from the server aggregate. Latency percentiles
  // are request-weighted averages of per-bucket percentiles (an honest
  // approximation, labelled as such).
  const windowTotals = useMemo(() => {
    if (!aggregate || aggregate.length === 0) return null
    let requests = 0, errors = 0, prompt = 0, completion = 0, cost = 0
    let p50w = 0, p95w = 0
    for (const b of aggregate) {
      requests += b.requests
      errors += b.errors
      prompt += b.prompt_tokens
      completion += b.completion_tokens
      cost += b.cost_cents
      p50w += b.latency_p50_ms * b.requests
      p95w += b.latency_p95_ms * b.requests
    }
    if (requests === 0) return null
    return {
      requests, errors, errPct: (errors / requests) * 100, prompt, completion, cost,
      p50: Math.round(p50w / requests), p95: Math.round(p95w / requests),
    }
  }, [aggregate])

  const activeFilters = FILTER_KEYS.filter((k) => f[k] !== FILTER_DEFAULTS[k]).length
  const clearFilters = () => { setSearch(''); setF({ team: '', alias: '', key: '', status: 'all', latency: '', q: '' }) }
  const currentQuery = () => {
    const params = new URLSearchParams()
    for (const k of FILTER_KEYS) if (f[k] !== FILTER_DEFAULTS[k]) params.set(k, f[k])
    return params.toString()
  }
  const applyView = (query: string) => {
    const params = new URLSearchParams(query)
    const patch: Record<string, string> = {}
    for (const k of FILTER_KEYS) patch[k] = params.get(k) ?? FILTER_DEFAULTS[k]
    setSearch(patch.q)
    setF(patch as Partial<typeof FILTER_DEFAULTS>)
  }
  const saveCurrentView = () => {
    const query = currentQuery()
    if (!query) { toast.error('Nothing to save', 'Set at least one filter first.'); return }
    const name = describeQuery(query)
    const next = [...views.filter((v) => v.query !== query), { name, query }].slice(-12)
    setViews(next); saveViews(next)
    toast.success('View saved', name)
  }
  const removeView = (query: string) => {
    const next = views.filter((v) => v.query !== query)
    setViews(next); saveViews(next)
  }

  const openRequest = useCallback((id: number) => setF({ request: String(id) }, { push: true }), [setF])
  const closeRequest = useCallback(() => setF({ request: '' }), [setF])
  const openId = f.request ? Number(f.request) : null
  const openRow = openId ? rows?.find((r) => r.id === openId) ?? null : null

  const filterControls = (
    <>
      {isAdmin && (
        <label className="select-shell" title="Filter by team">
          <UsersIcon size={14} />
          <select aria-label="Team" value={f.team} onChange={(e) => setF({ team: e.target.value })}>
            <option value="">All teams</option>
            {teams.map((t) => <option key={t.slug} value={t.slug}>{t.name}</option>)}
          </select>
        </label>
      )}
      <label className="select-shell" title="Filter by model alias">
        <Tag size={14} />
        <select aria-label="Alias" value={f.alias} onChange={(e) => setF({ alias: e.target.value })}>
          <option value="">All aliases</option>
          {(aliasCatalog.includes(f.alias) || !f.alias ? aliasCatalog : [f.alias, ...aliasCatalog]).map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
      </label>
      <label className="select-shell" title="Filter by latency">
        <Zap size={14} />
        <select aria-label="Latency" value={f.latency} onChange={(e) => setF({ latency: e.target.value as LatencyFilter })}>
          <option value="">Any latency</option>
          <option value="fast">Under 200 ms</option>
          <option value="med">200 ms to 1 s</option>
          <option value="slow">Over 1 s</option>
        </select>
      </label>
      <SegmentedFilter
        value={f.status as StatusFilter}
        onChange={(v) => setF({ status: v })}
        options={[
          { value: 'all', label: 'All', count: facets ? facets['2xx'] + facets['4xx'] + facets['5xx'] : undefined },
          { value: 'error', label: 'Errors', count: facets ? facets['4xx'] + facets['5xx'] : undefined, tone: 'err' },
          { value: '2xx', label: '2xx', count: facets?.['2xx'], tone: 'ok' },
          { value: '4xx', label: '4xx', count: facets?.['4xx'], tone: 'warn' },
          { value: '5xx', label: '5xx', count: facets?.['5xx'], tone: 'err' },
        ]}
      />
    </>
  )

  return (
    <>
      <PageHeader
        title="Request logs"
        description="Every request through the gateway."
        actions={
          <>
            <button
              className={'live-btn' + (live ? ' is-on' : '')}
              onClick={() => setLive((v) => !v)}
              title={live ? 'Pause live tail' : 'Tail incoming requests'}
            >
              {live ? <Pause size={13} /> : <Radio size={13} />}
              <span>{live ? 'Live' : 'Tail'}</span>
              {live && <span className="live-dot" />}
            </button>
            <SegmentedFilter
              variant="window"
              value={f.window as WindowChoice}
              onChange={(v) => setF({ window: v })}
              options={WINDOW_OPTIONS.map((w) => ({ value: w.value, label: w.label }))}
            />
            <Button variant="ghost" leadingIcon={<RefreshCcw size={13} />} onClick={reload}>Refresh</Button>
            <a className="btn btn-ghost" href="/admin/export/usage.csv" download>
              <Download size={13} /> Export
            </a>
          </>
        }
      />

      <div className="logs-toolbar">
        <label className="search-shell logs-search" title="Search request id, alias, team, deployment, key or user">
          <Search size={14} />
          <input
            aria-label="Search requests"
            placeholder="Search request id, alias, key, user…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          {search && (
            <button className="search-clear" onClick={() => setSearch('')} aria-label="Clear search"><X size={12} /></button>
          )}
        </label>
        <div className="logs-filters">{filterControls}</div>
        <button className="btn btn-ghost logs-filters-toggle" onClick={() => setFiltersOpen(true)}>
          <SlidersHorizontal size={14} /> Filters{activeFilters > 0 && <span className="tab-count">{activeFilters}</span>}
        </button>
        <RowMenu
          label="Views"
          trigger={<><Bookmark size={14} /> Views</>}
          items={[
            ...BUILTIN_VIEWS.map((v) => ({ label: v.name, onSelect: () => applyView(v.query) })),
            ...views.map((v) => ({ label: v.name, onSelect: () => applyView(v.query) })),
            { label: 'Save current filters as a view', onSelect: saveCurrentView },
            ...views.map((v) => ({ label: `Remove "${v.name}"`, destructive: true, onSelect: () => removeView(v.query) })),
          ]}
        />
        {activeFilters > 0 && <button className="linkish" onClick={clearFilters}>Clear filters</button>}
      </div>

      {filtersOpen && (
        <Drawer title="Filters" onClose={() => setFiltersOpen(false)} width={380}>
          <div className="filters-sheet">{filterControls}</div>
          <div className="flex gap-2">
            <Button fullWidth onClick={() => setFiltersOpen(false)}>Show {total.toLocaleString()} requests</Button>
          </div>
        </Drawer>
      )}

      <MetricStrip>
        <StatTile label="Requests" value={windowTotals ? windowTotals.requests.toLocaleString() : '—'} hint={`all traffic, last ${windowSpec.label}`} />
        <StatTile
          label="Errors"
          value={windowTotals ? windowTotals.errors.toLocaleString() : '—'}
          hint={windowTotals ? `${windowTotals.errPct.toFixed(windowTotals.errors === 0 ? 0 : 1)}% of requests` : `last ${windowSpec.label}`}
          tone={(windowTotals?.errors ?? 0) > 0 ? 'warning' : 'neutral'}
        />
        <StatTile label="p50 latency" value={windowTotals ? `${windowTotals.p50} ms` : '—'} hint="request-weighted" />
        <StatTile label="p95 latency" value={windowTotals ? `${windowTotals.p95} ms` : '—'} hint="request-weighted" />
        <StatTile
          label="Tokens"
          value={windowTotals ? compactCount(windowTotals.prompt + windowTotals.completion) : '—'}
          hint={windowTotals ? `${compactCount(windowTotals.prompt)} in, ${compactCount(windowTotals.completion)} out` : `last ${windowSpec.label}`}
        />
        <StatTile label="Estimated cost" value={windowTotals ? `$${(windowTotals.cost / 100).toFixed(2)}` : '—'} hint={`last ${windowSpec.label}`} />
      </MetricStrip>

      <Table
        head={[
          'Time',
          'Team',
          'Alias',
          'Key',
          <span className="cell-num" key="t">Tokens</span>,
          <span className="cell-num" key="l">Latency</span>,
          <span className="cell-num" key="$">Cost</span>,
          'Status',
          'Notes',
        ]}
        footer={
          <Pagination
            total={total}
            limit={limit}
            offset={offset}
            onChange={(p) => { setLimit(p.limit); setOffset(p.offset) }}
          />
        }
      >
        {rows === null ? (
          error ? (
            <ErrorRow cols={9} title="Couldn't load requests" message={error.message} onRetry={reload} retrying={refreshing} />
          ) : (
            <SkeletonRows />
          )
        ) : rows.length === 0 ? (
          <EmptyRow cols={9}>
            <Inbox size={18} />
            <span>{activeFilters > 0 ? 'No requests match these filters.' : `No requests in the last ${windowSpec.label}.`}</span>
            {activeFilters > 0 && <button className="linkish" onClick={clearFilters}>Clear filters</button>}
          </EmptyRow>
        ) : (
          rows.map((r) => (
            <UsageRequestRow key={r.id} row={r} selected={r.id === openId} onOpen={() => openRequest(r.id)} />
          ))
        )}
      </Table>

      {openId !== null && (
        <RequestDrawer id={openId} row={openRow} isAdmin={isAdmin} onClose={closeRequest} />
      )}
    </>
  )
}

// describeQuery names a saved view from its filters, e.g. "5xx · gpt-4".
function describeQuery(query: string): string {
  const p = new URLSearchParams(query)
  const parts: string[] = []
  if (p.get('status')) parts.push(p.get('status') === 'error' ? 'errors' : p.get('status')!)
  if (p.get('alias')) parts.push(p.get('alias')!)
  if (p.get('team')) parts.push(`team ${p.get('team')}`)
  if (p.get('latency')) parts.push({ fast: 'fast', med: 'medium latency', slow: 'slow' }[p.get('latency')!] ?? '')
  if (p.get('q')) parts.push(`"${p.get('q')}"`)
  return parts.filter(Boolean).join(', ') || 'Saved view'
}

function UsageRequestRow({ row, selected, onOpen }: { row: UsageRow; selected: boolean; onOpen: () => void }) {
  const status = row.status_code
  const tone = status < 300 ? 'ok' : status < 500 ? 'warn' : 'err'
  const latTone = row.latency_ms < 400 ? 'lat-ok' : row.latency_ms < 1200 ? 'lat-warn' : 'lat-err'
  const ts = new Date(row.ts)
  const tokens = row.prompt_tokens + row.completion_tokens

  return (
    <Tr expanded={selected} error={status >= 400} onClick={onOpen}>
      <Td className="tnum">
        <span title={`${ts.toLocaleString()} (${relativeTime(ts)})`}>
          {ts.toDateString() === new Date().toDateString()
            ? ts.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit', second: '2-digit' })
            : ts.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })}
        </span>
      </Td>
      <Td>
        <div className="cell-team-name" title={row.team_slug}>{row.team_name || row.team_slug}</div>
      </Td>
      <Td className="cell-alias mono">{row.alias}</Td>
      <Td className="mono">
        {row.key_prefix
          ? <span className="cell-key-prefix" title={row.user_email || undefined}>{row.key_prefix}…</span>
          : <span className="muted">—</span>}
      </Td>
      <Td num className="tnum">
        {tokens ? <span title={`${row.prompt_tokens.toLocaleString()} in, ${row.completion_tokens.toLocaleString()} out`}>{tokens.toLocaleString()}</span> : <span className="muted">—</span>}
      </Td>
      <Td num className={`tnum ${latTone}`}>{row.latency_ms.toLocaleString()} ms</Td>
      <Td num className="tnum">
        {row.accounting_state === 'unknown' || row.accounting_state === 'unpriced'
          ? <span className="muted" title={row.accounting_state === 'unpriced' ? 'No price is configured for this model' : 'Cost could not be determined'}>{row.accounting_state === 'unpriced' ? 'Unpriced' : 'Unknown'}</span>
          : <span title={row.accounting_state === 'estimated' ? 'Estimated from the request; final usage was not reported' : undefined}>{accountingCost(row)}{row.accounting_state === 'estimated' ? '*' : ''}</span>}
      </Td>
      <Td className="cell-status">
        <span className={`pill pill-${tone} tnum`}>{status}</span>
      </Td>
      <Td><RequestNotes row={row} /></Td>
    </Tr>
  )
}

const GUARDRAIL_LABEL: Record<string, string> = {
  block: 'Blocked', error: 'Guardrail error', unsupported: 'Unsupported', redact: 'Redacted', flag: 'Flagged',
}

// RequestNotes surfaces what happened on the way: served from cache, served
// by a fallback deployment, or touched by a guardrail. Empty when routine.
function RequestNotes({ row }: { row: UsageRow }) {
  const notes: { label: string; title: string }[] = []
  if (row.cached) notes.push({ label: 'Cached', title: 'Served from the prompt cache' })
  if (row.fallback) notes.push({ label: 'Fallback', title: `Served by ${row.deployment_name}, not the alias's primary deployment` })
  if (row.guardrail_decision) notes.push({ label: GUARDRAIL_LABEL[row.guardrail_decision] ?? row.guardrail_decision, title: 'Guardrail decision for this request' })
  if (notes.length === 0) return <span className="muted">—</span>
  return (
    <span className="notes">
      {notes.map((n) => <span key={n.label} className="note" title={n.title}>{n.label}</span>)}
    </span>
  )
}

// RequestDrawer shows one request. It uses the row from the loaded page when
// available and otherwise fetches it, so a shared permalink always opens.
function RequestDrawer({ id, row: known, isAdmin, onClose }: { id: number; row: UsageRow | null; isAdmin: boolean; onClose: () => void }) {
  const [fetched, setFetched] = useState<UsageRow | null>(null)
  const [missing, setMissing] = useState<string | null>(null)
  const row = known ?? fetched
  const toast = useToast()
  const navigate = useNavigate()

  useEffect(() => {
    if (known) return
    setFetched(null); setMissing(null)
    api.getUsageRow(id).then(setFetched).catch((e) => setMissing(e instanceof ApiError ? e.message : 'Could not load this request'))
  }, [id, known])

  const copyLink = () => {
    const url = `${window.location.origin}/usage?request=${id}`
    navigator.clipboard?.writeText(url)
    toast.success('Link copied')
  }

  return (
    <Drawer
      title={row ? `${row.status_code} · ${row.alias}` : 'Request'}
      subtitle={row ? <span className="mono">{row.request_id || `req_${row.id}`}</span> : undefined}
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" size="sm" leadingIcon={<Link2 size={13} />} onClick={copyLink}>Copy link</Button>
          {row && (
            <Button
              variant="ghost"
              size="sm"
              leadingIcon={<MessageSquare size={13} />}
              onClick={() => navigate(`/playground?alias=${encodeURIComponent(row.alias)}&from=${row.id}`)}
            >
              Open in Playground
            </Button>
          )}
        </>
      }
    >
      {missing ? <p className="muted">{missing}</p> : row ? <RequestDetail row={row} isAdmin={isAdmin} /> : <p className="muted">Loading request…</p>}
    </Drawer>
  )
}

function RequestDetail({ row, isAdmin }: { row: UsageRow; isAdmin: boolean }) {
  const [payload, setPayload] = useState<UsagePayload | null>(null)
  const [payloadErr, setPayloadErr] = useState<string | null>(null)
  const [loadingPayload, setLoadingPayload] = useState(false)
  const [replay, setReplay] = useState<ReplayResponse | null>(null)
  const [replaying, setReplaying] = useState(false)
  const toast = useToast()

  const runReplay = async () => {
    setReplaying(true)
    try {
      const resp = await api.replayUsage(row.id)
      setReplay(resp)
      if (!resp.replayed) toast.error('Replay skipped', resp.reason)
      else if (resp.status_code >= 400) toast.error(`Replay returned ${resp.status_code}`)
      else toast.success(`Replay returned ${resp.status_code} in ${resp.latency_ms} ms`)
    } catch (e) {
      toast.error('Replay failed', e instanceof ApiError ? e.message : undefined)
    } finally {
      setReplaying(false)
    }
  }

  useEffect(() => {
    setPayload(null); setPayloadErr(null)
    if (!isAdmin || !row.has_payload) return
    setLoadingPayload(true)
    api.getUsagePayload(row.id)
      .then(setPayload)
      .catch((e) => setPayloadErr(e instanceof ApiError ? e.message : 'failed to load'))
      .finally(() => setLoadingPayload(false))
  }, [row.id, row.has_payload, isAdmin])

  const copyCurl = () => {
    const body = payload?.request_body
    const baseURL = `${window.location.origin}/v1`
    const cmd = body
      ? `curl ${baseURL}/chat/completions \\\n  -H "Authorization: Bearer $GATEMUX_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(body)}'`
      : `curl ${baseURL}/chat/completions \\\n  -H "Authorization: Bearer $GATEMUX_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${row.alias}","messages":[]}'`
    navigator.clipboard?.writeText(cmd)
    toast.success(body ? 'Copied curl command' : 'Copied curl template (body was not captured)')
  }

  const copyJSON = () => {
    navigator.clipboard?.writeText(JSON.stringify(payload?.request_body ?? row, null, 2))
    toast.success(payload ? 'Copied request body' : 'Copied request metadata (body was not captured)')
  }

  return (
    <>
      {row.error && <DenialPanel row={row} />}

      <div className="detail-toolbar">
        {isAdmin && (
          <button
            className="btn btn-ghost btn-size-sm"
            onClick={runReplay}
            disabled={replaying || !row.has_payload}
            title={row.has_payload ? 'Re-run this request through current routing' : 'Body capture was off for this request'}
          >
            <Play size={12} /> {replaying ? 'Replaying…' : 'Replay'}
          </button>
        )}
        <button className="btn btn-ghost btn-size-sm" onClick={copyCurl}><Copy size={12} /> Copy as curl</button>
        <button className="btn btn-ghost btn-size-sm" onClick={copyJSON}><Download size={12} /> Copy JSON</button>
      </div>

      {replay && <ReplayResultCard original={row} replay={replay} />}

      <div className="detail-row-1">
        <TimingBreakdownCard row={row} />
        <RoutingCard row={row} />
      </div>
      <div className="detail-row-1">
        <CallerCard row={row} />
        <TokensCard row={row} />
      </div>
      {isAdmin
        ? <RequestBodyCard row={row} payload={payload} loading={loadingPayload} error={payloadErr} />
        : <section className="detail-card"><div className="detail-card-h">Request body</div><p className="muted">Captured payloads and replay are available only to administrators.</p></section>}
    </>
  )
}

function SkeletonRows() {
  return (
    <>
      {Array.from({ length: 8 }).map((_, i) => (
        <tr key={i} className="data-row">
          {Array.from({ length: 10 }).map((__, j) => (
            <td key={j}>
              <span className="skel" style={{ width: j === 0 ? 14 : j === 5 || j === 6 ? 50 : 80, display: 'block' }} />
            </td>
          ))}
        </tr>
      ))}
    </>
  )
}

function relativeTime(d: Date) {
  const diff = (Date.now() - d.getTime()) / 1000
  if (diff < 60) return `${Math.round(diff)}s ago`
  if (diff < 3600) return `${Math.round(diff / 60)}m ago`
  if (diff < 86400) return `${Math.round(diff / 3600)}h ago`
  return `${Math.round(diff / 86400)}d ago`
}

type Denial = {
  title: string
  detail: string
  kind: string
  fix?: { label: string; to: string }
}

const SCOPE_LABEL: Record<string, string> = { key: 'key', team: 'team', user: 'user', customer: 'customer' }

// explainDenial turns a gateway refusal code into what happened, which
// layer refused, and where that setting lives. Codes it doesn't know fall
// back to the raw message.
function explainDenial(row: UsageRow): Denial | null {
  const code = (row.error ?? '').trim()
  if (!code) return null
  const [head, scope = ''] = code.split(':', 2)
  const team = row.team_slug ? `/teams/${encodeURIComponent(row.team_slug)}` : '/teams'
  switch (head) {
    case 'budget_exceeded': {
      const who = SCOPE_LABEL[scope] ?? 'scope'
      const fix = scope === 'key' ? { label: 'Open the team’s keys', to: `${team}?tab=keys` }
        : scope === 'user' ? { label: 'Open the user', to: `/users?q=${encodeURIComponent(row.user_email ?? '')}` }
        : scope === 'customer' ? { label: 'Open the team’s customers', to: `${team}?tab=customers` }
        : { label: 'Open budget & limits', to: `${team}?tab=limits` }
      return { kind: 'Budget', title: `The ${who} budget is used up`, detail: `This ${who} has spent its budget for the current period, so the gateway refused the request before calling a provider. Requests resume when the period resets or the budget is raised.`, fix }
    }
    case 'rate_limited': {
      const what = scope.replace(/_/g, ' ') || 'rate limit'
      return { kind: 'Rate limit', title: `Rate limit reached (${what})`, detail: 'Too many requests or tokens in the current minute. The client was told when to retry; nothing reached a provider.', fix: scope.startsWith('customer') ? { label: 'Open the team’s customers', to: `${team}?tab=customers` } : { label: 'Open budget & limits', to: `${team}?tab=limits` } }
    }
    case 'model_not_allowed':
      return { kind: 'Model access', title: 'This model isn’t allowed for the caller', detail: 'The alias isn’t in the models allowed for both the team and the key.', fix: { label: 'Open model access', to: `${team}?tab=limits` } }
    case 'concurrency_limit_unavailable':
    case 'distributed concurrency check unavailable':
      return { kind: 'Concurrency', title: 'Concurrency limits couldn’t be checked', detail: 'The shared concurrency store didn’t answer, and limits fail closed, so the request was refused rather than risk going over a cap. Check Redis.', fix: { label: 'Open limits', to: '/concurrency' } }
    case 'all eligible deployments are at concurrency capacity':
      return { kind: 'Capacity', title: 'Every deployment for this model was full', detail: 'All deployments behind the alias were at their in-flight limit.', fix: { label: 'Open limits', to: '/concurrency' } }
    case 'no healthy deployment':
      return { kind: 'Routing', title: 'No healthy deployment could serve this model', detail: 'Every deployment behind the alias was unready or had its circuit open.', fix: { label: 'Open deployments', to: '/models?tab=deployments' } }
    case 'key_accounting_unsupported':
    case 'customer_accounting_unsupported':
      return { kind: 'Budget', title: 'This request type can’t be metered for a budget', detail: `The ${head.startsWith('key') ? 'key' : 'customer'} has a budget, and this endpoint can’t be priced reliably, so it was refused rather than let spend go uncounted.`, fix: head.startsWith('key') ? { label: 'Open the team’s keys', to: `${team}?tab=keys` } : { label: 'Open the team’s customers', to: `${team}?tab=customers` } }
    case 'guardrail_blocked':
      return { kind: 'Guardrail', title: 'A guardrail blocked this request', detail: 'The prompt or response matched a blocking rule for the team or model.', fix: { label: 'Open guardrails', to: '/guardrails' } }
    case 'guardrail_unsupported':
      return { kind: 'Guardrail', title: 'Guardrails can’t inspect this request', detail: 'The team or model has guardrails, and this request used content they can’t check (tools, images or a native API), so it was refused.', fix: { label: 'Open guardrails', to: '/guardrails' } }
    case 'guardrail_unavailable':
      return { kind: 'Guardrail', title: 'Guardrails were unavailable', detail: 'Rules couldn’t be evaluated, and guardrails fail closed.', fix: { label: 'Open guardrails', to: '/guardrails' } }
    case 'customer_required':
    case 'customer_not_registered':
    case 'invalid_customer':
    case 'customer_archived':
      return { kind: 'Customer', title: { customer_required: 'This team requires a customer ID', customer_not_registered: 'The customer ID isn’t registered', invalid_customer: 'The customer ID is invalid', customer_archived: 'The customer is archived' }[head]!, detail: 'The team’s customer registration policy refused the request before it reached a provider.', fix: { label: 'Open the team’s customers', to: `${team}?tab=customers` } }
    case 'request_interrupted_or_unrecorded':
      return { kind: 'Interrupted', title: 'The request didn’t finish cleanly', detail: 'The gateway restarted or lost the connection before recording a result. Any spend was reserved conservatively.' }
    case 'unknown model alias':
      return { kind: 'Routing', title: 'No model has this name', detail: `The caller asked for “${row.alias}”, which isn’t an alias on this gateway.`, fix: { label: 'Open models', to: '/models' } }
    default:
      if (code.startsWith('upstream') && code.includes('timeout')) return { kind: 'Provider', title: 'The provider didn’t respond in time', detail: 'The provider stopped sending before the gateway’s timeout, so the request was cut off. Slow models may need a longer stream timeout on the deployment.', fix: { label: 'Open deployments', to: '/models?tab=deployments' } }
      if (row.status_code >= 502 && row.status_code <= 504) {
        const unreachable = /connection refused|no such host|dial tcp|EOF/i.test(code)
        return {
          kind: 'Provider',
          title: unreachable ? 'The provider couldn’t be reached' : 'The provider returned an error',
          detail: unreachable
            ? 'The gateway couldn’t open a connection to the deployment’s address. Check that the provider is running and the base URL is right; the raw error is below.'
            : 'The provider answered with an error, so no reply was returned. The raw error is below.',
          fix: { label: 'Open deployments', to: '/models?tab=deployments' },
        }
      }
      return null
  }
}

// DenialPanel explains a failed request in one block: what happened, the
// layer that refused it, the raw code, and where to change the setting.
function DenialPanel({ row }: { row: UsageRow }) {
  const d = explainDenial(row)
  const serverSide = row.status_code >= 500
  return (
    <section className={'denial' + (serverSide ? ' is-server' : '')} aria-label={d ? 'Why this request failed' : 'Error'}>
      <div className="denial-head">
        <AlertTriangle size={15} aria-hidden="true" />
        <strong>{d ? d.title : 'Request failed'}</strong>
        {d && <span className="denial-kind">{d.kind}</span>}
      </div>
      {d && <p className="denial-detail">{d.detail}</p>}
      <dl className="denial-meta">
        <div><dt>Status</dt><dd className="mono">{row.status_code}</dd></div>
        <div><dt>Code</dt><dd className="mono">{row.error}</dd></div>
      </dl>
      {d?.fix && <Link className="linkish denial-fix" to={d.fix.to}>{d.fix.label}</Link>}
    </section>
  )
}

function ReplayResultCard({ original, replay }: { original: UsageRow; replay: ReplayResponse }) {
  if (!replay.replayed) {
    return (
      <section className="data-card" style={{ padding: 12, marginTop: 10 }}>
        <div className="flex items-center gap-2">
          <Play size={13} />
          <span className="data-h" style={{ fontSize: 13 }}>Replay skipped</span>
        </div>
        <div className="muted" style={{ marginTop: 4, fontSize: 12 }}>{replay.reason}</div>
      </section>
    )
  }
  const sameStatus = replay.status_code === original.status_code
  const tone = replay.status_code >= 500 ? 'danger' : replay.status_code >= 400 ? 'warning' : 'success'
  const bodyText = (() => {
    const b = replay.response_body
    if (b == null) return ''
    if (typeof b === 'string') return b
    try {
      return JSON.stringify(b, null, 2)
    } catch {
      return String(b)
    }
  })()
  return (
    <section className="data-card" style={{ padding: 12, marginTop: 10 }}>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Play size={13} />
          <span className="data-h" style={{ fontSize: 13 }}>Replay result</span>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={tone}>HTTP {replay.status_code}</Badge>
          <span className="muted tnum" style={{ fontSize: 12 }}>{replay.latency_ms} ms</span>
          <span className="muted mono" style={{ fontSize: 11 }}>{replay.endpoint_kind}</span>
        </div>
      </div>
      <div className="kv-flat" style={{ marginTop: 8 }}>
        <dt>Original</dt>
        <dd className="mono">HTTP {original.status_code} · {original.latency_ms} ms</dd>
        <dt>Replay</dt>
        <dd className="mono">HTTP {replay.status_code} · {replay.latency_ms} ms</dd>
        <dt>Verdict</dt>
        <dd>
          {sameStatus ? (
            <span className="muted">Same outcome as original</span>
          ) : (
            <span style={{ color: tone === 'success' ? 'var(--success-fg)' : 'var(--danger-fg)' }}>
              {original.status_code >= 400 && replay.status_code < 400 ? 'Recovered — original failed, replay succeeded'
                : original.status_code < 400 && replay.status_code >= 400 ? 'Regressed — original succeeded, replay failed'
                : `Outcome changed (${original.status_code} → ${replay.status_code})`}
            </span>
          )}
        </dd>
        {replay.request_id && (
          <>
            <dt>Replay request id</dt>
            <dd className="mono">{replay.request_id}</dd>
          </>
        )}
      </div>
      {bodyText && (
        <pre style={{ marginTop: 10, padding: 10, background: 'var(--surface-2, rgba(127,127,127,0.06))', borderRadius: 6, fontSize: 11.5, maxHeight: 240, overflow: 'auto' }}>
          <code>{bodyText}</code>
        </pre>
      )}
    </section>
  )
}

function TimingBreakdownCard({ row }: { row: UsageRow }) {
  // Three named phases match the screenshot's visual: Gateway (queue =
  // pre-call work), Upstream (provider HTTP call), Stream (postprocess
  // / response write). Legacy rows fall back to a single bar.
  const gateway = row.queue_ms ?? 0
  const upstream = row.upstream_ms ?? 0
  const stream = row.postprocess_ms ?? 0
  const tracked = gateway + upstream + stream
  const segments = [
    { label: 'Gateway',  value: gateway,  cls: 'timing-gateway' },
    { label: 'Upstream', value: upstream, cls: 'timing-upstream' },
    { label: 'Stream',   value: stream,   cls: 'timing-stream' },
  ].filter((s) => s.value > 0)

  return (
    <section className="detail-card">
      <div className="detail-card-h">Timing</div>
      <div className="timing-bar">
        {tracked === 0 ? (
          <span className="timing-seg timing-gateway" style={{ flex: 1 }} />
        ) : (
          segments.map((s) => (
            <span key={s.label} className={`timing-seg ${s.cls}`} style={{ flex: s.value }} title={`${s.label} ${s.value}ms`} />
          ))
        )}
      </div>
      <div className="timing-legend">
        {(['Gateway', 'Upstream', 'Stream'] as const).map((label) => {
          const value = label === 'Gateway' ? gateway : label === 'Upstream' ? upstream : stream
          const cls = label === 'Gateway' ? 'timing-gateway' : label === 'Upstream' ? 'timing-upstream' : 'timing-stream'
          return (
            <div key={label} className="timing-legend-item">
              <span className={`timing-dot ${cls}`} />
              <span className="muted small">{label}</span>
              <span className="mono tnum">{value > 0 ? `${value}ms` : '—'}</span>
            </div>
          )
        })}
      </div>
      {tracked === 0 && (
        <div className="muted small" style={{ marginTop: 8 }}>
          Breakdown not captured for this row · total {row.latency_ms}ms
        </div>
      )}
      <div className="timing-ttft">
        <span className="muted small">TTFT</span>
        <span className="mono tnum">{row.ttfb_ms != null ? `${row.ttfb_ms}ms` : '—'}</span>
      </div>
    </section>
  )
}

function RoutingCard({ row }: { row: UsageRow }) {
  return (
    <section className="detail-card">
      <div className="detail-card-h">Routing</div>
      <dl className="kv-flat">
        <div><dt>Alias</dt><dd className="mono">{row.alias}</dd></div>
        <div><dt>Deployment</dt><dd className="mono">{row.deployment_name || <span className="muted">—</span>}</dd></div>
        <div><dt>Provider</dt><dd className="mono">{row.provider_type || <span className="muted">—</span>}</dd></div>
        <div><dt>Strategy</dt><dd className="mono">{row.strategy || 'priority'}</dd></div>
        <div><dt>Cached</dt><dd className="mono">{row.cached ? 'yes' : 'no'}</dd></div>
      </dl>
    </section>
  )
}

function CallerCard({ row }: { row: UsageRow }) {
  return (
    <section className="detail-card">
      <div className="detail-card-h">Caller</div>
      <dl className="kv-flat">
        <div>
          <dt>Team</dt>
          <dd>
            {row.team_name || row.team_slug}
            {row.team_name && <span className="muted mono"> ({row.team_slug})</span>}
          </dd>
        </div>
        <div><dt>User</dt><dd className="mono">{row.user_email || <span className="muted">—</span>}</dd></div>
        <div><dt>Key</dt><dd className="mono">{row.key_prefix ? `${row.key_prefix}…` : <span className="muted">—</span>}</dd></div>
        <div><dt>Key label</dt><dd className="mono">{row.key_name || <span className="muted">—</span>}</dd></div>
        <div><dt>IP</dt><dd className="mono">{row.client_ip || <span className="muted">—</span>}</dd></div>
        {row.customer_external_id && (
          <div><dt>Customer</dt><dd className="mono">{row.customer_external_id}</dd></div>
        )}
      </dl>
      {row.request_tags && row.request_tags.length > 0 && (
        <div className="chip-row" style={{ marginTop: 8 }}>
          {row.request_tags.map((t) => (
            <Badge key={t} tone="neutral">{t}</Badge>
          ))}
        </div>
      )}
    </section>
  )
}

function compactCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${Math.round(n / 1000)}k`
  return n.toLocaleString()
}

function accountingCost(row: UsageRow) {
  if (row.accounting_state === 'unknown') return 'Unknown'
  if (row.accounting_state === 'unpriced') return 'Unpriced'
  return `$${(row.cost_cents / 100).toFixed(4)}`
}

function TokensCard({ row }: { row: UsageRow }) {
  const promptFlex = Math.max(row.prompt_tokens, 1)
  const completionFlex = Math.max(row.completion_tokens, 0)
  return (
    <section className="detail-card">
      <div className="detail-card-h">Tokens and cost</div>
      <div className="tokens-bar" style={{ marginBottom: 12 }}>
        <span className="tok-seg tok-1" style={{ flex: promptFlex }}>
          {row.prompt_tokens.toLocaleString()} prompt
        </span>
        {row.completion_tokens > 0 && (
          <span className="tok-seg tok-3" style={{ flex: completionFlex }}>
            {row.completion_tokens.toLocaleString()} completion
          </span>
        )}
      </div>
      <dl className="kv-flat">
        <div><dt>Prompt</dt><dd className="mono tnum">{row.prompt_tokens.toLocaleString()} tok</dd></div>
        <div><dt>Completion</dt><dd className="mono tnum">{row.completion_tokens.toLocaleString()} tok</dd></div>
        <div><dt>Recorded cost</dt><dd className="mono tnum">{accountingCost(row)}</dd></div>
        <div><dt>Accounting evidence</dt><dd>{row.accounting_state ?? 'legacy'}</dd></div>
        <div><dt>Latency</dt><dd className="mono tnum">{row.latency_ms}ms</dd></div>
      </dl>
      {['unknown', 'unpriced', 'estimated', 'legacy'].includes(row.accounting_state ?? 'legacy') && <p className="muted">This is not a verified provider invoice amount. Unknown and unpriced calls may incur charges not included in spend totals; estimates retain conservative reservations.</p>}
    </section>
  )
}

function RequestBodyCard({
  row,
  payload,
  loading,
  error,
}: {
  row: UsageRow
  payload: UsagePayload | null
  loading: boolean
  error: string | null
}) {
  const [view, setView] = useState<'conversation' | 'json'>('conversation')
  const [captureOn, setCaptureOn] = useState<boolean | null>(null)
  useEffect(() => {
    if (row.has_payload || !row.team_slug) return
    let alive = true
    api.getTeam(row.team_slug).then((t) => { if (alive) setCaptureOn(Boolean(t.capture_payloads)) }).catch(() => {})
    return () => { alive = false }
  }, [row.has_payload, row.team_slug])

  if (!row.has_payload) {
    return (
      <section className="detail-card">
        <div className="detail-card-h">Request body</div>
        {captureOn ? (
          <p className="muted small">
            Capture is on for this team, but this request has no body. Requests refused before reaching a provider
            (budget, rate limit or model access) and cache hits aren’t captured.
          </p>
        ) : (
          <p className="muted small">
            Body capture is off for team <code className="mono">{row.team_slug}</code>. Turn it on in{' '}
            <Link className="linkish" to={`/teams/${row.team_slug}?tab=settings`}>the team’s Data privacy settings</Link>{' '}
            to see prompts and responses for new requests.
          </p>
        )}
      </section>
    )
  }
  const chat = payload ? readChat(payload) : null
  return (
    <section className="detail-card">
      <div className="payload-head">
        <div className="detail-card-h" style={{ margin: 0 }}>Request and response</div>
        {chat && (
          <SegmentedFilter
            variant="window"
            value={view}
            onChange={setView}
            options={[{ value: 'conversation', label: 'Conversation' }, { value: 'json', label: 'JSON' }]}
          />
        )}
      </div>
      {loading && <div className="muted small">Loading the captured body…</div>}
      {error && <div className="muted small" style={{ color: 'var(--danger-fg)' }}>{error}</div>}
      {payload && chat && view === 'conversation' && <ConversationView chat={chat} />}
      {payload && (!chat || view === 'json') && (
        <>
          <div className="payload-label">Request</div>
          <pre className="mono payload-pre">{prettyJSON(payload.request_body)}</pre>
          <div className="payload-label">Response</div>
          {payload.response_body == null
            ? <p className="muted small" style={{ margin: 0 }}>No response body: the request failed before a reply arrived.</p>
            : <pre className="mono payload-pre">{prettyJSON(payload.response_body)}</pre>}
        </>
      )}
    </section>
  )
}

type ChatTurn = { role: string; text: string; extra?: string }
type ReadChat = { params: [string, string][]; turns: ChatTurn[]; reply: ChatTurn | null; replyNote: string | null }

// readChat pulls a chat-completions exchange out of the captured bodies.
// Anything else (Responses, embeddings) returns null and shows as JSON.
function readChat(payload: UsagePayload): ReadChat | null {
  const req = payload.request_body as Record<string, unknown> | null
  if (!req || !Array.isArray(req.messages)) return null
  const text = (content: unknown): { text: string; extra?: string } => {
    if (typeof content === 'string') return { text: content }
    if (Array.isArray(content)) {
      const parts = content as { type?: string; text?: string }[]
      const others = parts.filter((p) => p.type !== 'text').length
      return { text: parts.filter((p) => p.type === 'text').map((p) => p.text ?? '').join('\n'), extra: others ? `${others} non-text part${others === 1 ? '' : 's'}` : undefined }
    }
    return { text: content == null ? '' : JSON.stringify(content) }
  }
  const turns = (req.messages as Record<string, unknown>[]).map((m) => {
    const t = text(m.content)
    const calls = Array.isArray(m.tool_calls) ? `${m.tool_calls.length} tool call${m.tool_calls.length === 1 ? '' : 's'}` : undefined
    return { role: String(m.role ?? 'message'), text: t.text, extra: [t.extra, calls].filter(Boolean).join(', ') || undefined }
  })
  const params: [string, string][] = []
  for (const k of ['temperature', 'top_p', 'max_tokens', 'max_completion_tokens', 'seed', 'user']) {
    if (req[k] != null) params.push([k.replace(/_/g, ' '), String(req[k])])
  }
  if (req.stream) params.push(['stream', 'yes'])
  if (Array.isArray(req.tools)) params.push(['tools', String(req.tools.length)])
  if (req.response_format && typeof req.response_format === 'object') params.push(['response format', String((req.response_format as { type?: string }).type ?? 'set')])
  if (req.metadata && typeof req.metadata === 'object') params.push(['metadata', Object.entries(req.metadata as Record<string, unknown>).map(([k, v]) => `${k}=${v}`).join(', ')])

  const resp = payload.response_body as Record<string, unknown> | null
  let reply: ChatTurn | null = null
  const notes: string[] = []
  if (resp && Array.isArray(resp.choices) && resp.choices[0]) {
    const c = resp.choices[0] as { message?: Record<string, unknown>; finish_reason?: string }
    const t = text(c.message?.content)
    const calls = Array.isArray(c.message?.tool_calls) ? `${(c.message!.tool_calls as unknown[]).length} tool call(s)` : undefined
    reply = { role: 'assistant', text: t.text, extra: [t.extra, calls].filter(Boolean).join(', ') || undefined }
    if (c.finish_reason) notes.push(`finished: ${c.finish_reason}`)
  }
  if (resp?.streamed) notes.push('reassembled from the stream')
  if (resp?.truncated) notes.push('reply cut at 256 KB')
  if (!resp) notes.push('no reply: the request failed before a response arrived')
  return { params, turns, reply, replyNote: notes.join(' · ') || null }
}

function ConversationView({ chat }: { chat: ReadChat }) {
  return (
    <div className="convo">
      {chat.params.length > 0 && (
        <dl className="convo-params">
          {chat.params.map(([k, v]) => <div key={k}><dt>{k}</dt><dd className="mono">{v}</dd></div>)}
        </dl>
      )}
      <ol className="convo-turns">
        {chat.turns.map((t, i) => <TurnItem key={i} turn={t} />)}
        {chat.reply && <TurnItem turn={chat.reply} isReply />}
      </ol>
      {chat.replyNote && <p className="muted small" style={{ margin: 0 }}>{chat.replyNote}</p>}
    </div>
  )
}

function TurnItem({ turn, isReply = false }: { turn: ChatTurn; isReply?: boolean }) {
  return (
    <li className={'convo-turn role-' + turn.role + (isReply ? ' is-reply' : '')}>
      <div className="convo-role">{isReply ? 'Reply' : turn.role.charAt(0).toUpperCase() + turn.role.slice(1)}</div>
      <div className="convo-text">{turn.text || <span className="muted">(empty)</span>}</div>
      {turn.extra && <div className="muted small">{turn.extra}</div>}
    </li>
  )
}

function prettyJSON(v: unknown): string {
  if (v === undefined || v === null) return ''
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

// Activity + Badge are referenced by the empty/detail rows above; mention
// them here too so removing them from imports doesn't silently break.
void Activity
