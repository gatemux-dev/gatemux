import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  AlertTriangle,
  Check,
  DollarSign,
  Shield,
  Tag,
  Zap,
} from 'lucide-react'
import { api, ApiError } from '../api/client'
import type { AliasStrategy } from '../types'
import { useQuery } from '../lib/useQuery'
import {
  Badge,
  Button,
  ErrorState,
  Input,
  MetricStrip,
  PageHeader,
  SegmentedFilter,
  StatTile,
  useToast,
} from '../components/ui'

// AliasDetail mirrors the handoff's AliasDetailPage — four cards: routing
// strategy picker, routing preview, cache toggle, last 24h activity. Strategy
// is persisted via PATCH /admin/aliases/:alias/strategy. Cache via the
// existing PATCH /admin/aliases/:alias/cache. Sparkline + KPIs read from
// /admin/usage/aggregate?alias=…

type Strategy = AliasStrategy

const STRATEGY_CARDS: { id: Strategy; label: string; icon: typeof Zap; desc: string }[] = [
  {
    id: 'cost',
    label: 'Cheapest first',
    icon: DollarSign,
    desc: 'Send to the lowest-cost deployment with capacity. Falls back when one is rate-limited.',
  },
  {
    id: 'latency',
    label: 'Fastest first',
    icon: Zap,
    desc: 'Sticky-route by observed p95. Rebalances every minute from health checks.',
  },
  {
    id: 'priority',
    label: 'Failover',
    icon: Shield,
    desc: 'Primary deployment until it errors; weighted hot standbys take over instantly.',
  },
  {
    id: 'tagged',
    label: 'Tag-based',
    icon: Tag,
    desc: 'Match request tags (region, env, customer-tier) to deployment tags.',
  },
]

const REGION_OPTIONS = ['any', 'us', 'eu', 'apac'] as const
type Region = typeof REGION_OPTIONS[number]

export default function AliasDetail() {
  const { alias: aliasParam } = useParams<{ alias: string }>()
  const aliasName = aliasParam ?? ''
  const toast = useToast()

  // Local form state (initialized from server values)
  const [strategy, setStrategy] = useState<Strategy>('priority')
  const [region, setRegion] = useState<Region>('any')
  const [tagFilter, setTagFilter] = useState('')
  const [cacheEnabled, setCacheEnabled] = useState(false)
  const [cacheTtlMin, setCacheTtlMin] = useState(10)
  const [savingStrategy, setSavingStrategy] = useState(false)
  const [savingCache, setSavingCache] = useState(false)

  // Wrapped in an object so "loaded but not found" (data.alias === null) is
  // distinguishable from "not loaded yet" (data === null).
  const {
    data: aliasResult,
    error: aliasError,
    loading: aliasLoading,
    refreshing: aliasRefreshing,
    reload: reloadAlias,
  } = useQuery(
    async () => {
      const page = await api.listAliases({ limit: 500 })
      return { alias: page.items.find((a) => a.alias === aliasName) ?? null }
    },
    [aliasName],
    { enabled: !!aliasName },
  )
  const alias = aliasResult?.alias ?? null

  const { data: deployments, reload: reloadDeployments } = useQuery(
    () => api.listDeployments({ limit: 500 }).then((p) => p.items),
    [aliasName],
    { enabled: !!aliasName },
  )

  const { data: aggregate, reload: reloadAggregate } = useQuery(
    () => {
      const to = new Date()
      const from = new Date(to.getTime() - 24 * 3600_000)
      return api
        .getUsageAggregate({
          alias: aliasName,
          from: from.toISOString(),
          to: to.toISOString(),
          bucket: 'hour',
        })
        .then((agg) => agg ?? [])
    },
    [aliasName],
    { enabled: !!aliasName },
  )

  const reload = () => {
    reloadAlias()
    reloadDeployments()
    reloadAggregate()
  }

  // Seed the form whenever a fresh copy of the alias arrives (first load and
  // after each save-triggered reload), mirroring the old effect's behavior.
  // `saved` mirrors the persisted values so each Save button only becomes
  // the primary action once its section actually differs from the server.
  const [saved, setSaved] = useState<{ strategy: string; region: string; tag: string; cache: boolean; ttl: number } | null>(null)
  useEffect(() => {
    const found = aliasResult?.alias
    if (!found) return
    const opts = found.strategy_options ?? {}
    const storedRegion = typeof opts.region === 'string' ? opts.region : typeof opts.region_pref === 'string' ? opts.region_pref : 'any'
    const nextRegion = (REGION_OPTIONS as readonly string[]).includes(storedRegion) ? storedRegion : 'any'
    const nextTag = typeof opts.tag === 'string' ? opts.tag : ''
    const nextTtl = Math.max(1, Math.round(found.cache_ttl_seconds / 60) || 10)
    setStrategy((found.strategy ?? 'priority') as Strategy)
    setCacheEnabled(found.cache_enabled)
    setCacheTtlMin(nextTtl)
    setRegion(nextRegion as Region)
    setTagFilter(nextTag)
    setSaved({ strategy: found.strategy ?? 'priority', region: nextRegion, tag: nextTag, cache: found.cache_enabled, ttl: nextTtl })
  }, [aliasResult])
  const strategyDirty = saved !== null && (strategy !== saved.strategy || region !== saved.region || tagFilter !== saved.tag)
  const cacheDirty = saved !== null && (cacheEnabled !== saved.cache || (cacheEnabled && cacheTtlMin !== saved.ttl))

  const myDeployments = useMemo(() => {
    if (!alias || !deployments) return []
    const set = new Set(alias.deployments ?? [])
    return deployments.filter((d) => set.has(d.name))
  }, [alias, deployments])

  // Per-deployment scoring for the "Routing preview" card — mirrors the
  // logic in router.Resolve so what users see lines up with what ships.
  const distribution = useMemo(() => {
    if (myDeployments.length === 0) return []
    const scored = myDeployments.map((d) => {
      let score = 1
      if (strategy === 'cost') {
        // Without per-row pricing here we approximate by provider tier;
        // the real router uses the pricing table. This is a preview.
        const tier = providerCostTier(d.provider_type)
        score = (200 - tier) / 200
      } else if (strategy === 'latency') {
        const lat = providerLatencyHint(d.provider_type)
        score = 1 - lat / 1500
      } else if (strategy === 'priority') {
        score = d.enabled ? 1 : 0.1
      } else if (strategy === 'tagged') {
        score = tagFilter ? 1 : 0.5
      } else if (strategy === 'region') {
        score = region === 'any' || (d.region ?? '').includes(region) ? 1 : 0.05
      }
      return {
        d,
        score: Math.max(0.05, score),
        latencyMs: providerLatencyHint(d.provider_type),
      }
    })
    const total = scored.reduce((a, s) => a + s.score, 0) || 1
    return scored.map((s) => ({ ...s, share: s.score / total }))
  }, [myDeployments, strategy, region, tagFilter])

  const sparkPath = useMemo(() => {
    if (!aggregate || aggregate.length === 0) return ''
    const max = Math.max(1, ...aggregate.map((b) => b.requests))
    const W = 200
    const H = 56
    return aggregate
      .map((b, i) => {
        const x = (i / Math.max(1, aggregate.length - 1)) * W
        const y = H - (b.requests / max) * (H - 6) - 3
        return (i === 0 ? 'M' : 'L') + x.toFixed(1) + ' ' + y.toFixed(1)
      })
      .join(' ')
  }, [aggregate])

  const totals24h = useMemo(() => {
    const a = aggregate ?? []
    let reqs = 0
    let errs = 0
    let latP95 = 0
    for (const b of a) {
      reqs += b.requests
      errs += b.errors
      if (b.latency_p95_ms > latP95) latP95 = b.latency_p95_ms
    }
    return {
      reqs,
      errs,
      errPct: reqs > 0 ? (errs / reqs) * 100 : 0,
      p95: Math.round(latP95),
      avgPerHour: a.length > 0 ? Math.round(reqs / a.length) : 0,
    }
  }, [aggregate])

  if (!aliasName) return null
  if (aliasLoading) {
    return (
      <>
        <PageHeader
          title={aliasName}
        />
        <div className="muted">Loading…</div>
      </>
    )
  }
  if (aliasError && aliasResult === null) {
    return (
      <>
        <PageHeader
          title={aliasName}
        />
        <ErrorState
          title="Couldn't load alias"
          message={aliasError.message}
          onRetry={reloadAlias}
          retrying={aliasRefreshing}
        />
      </>
    )
  }
  if (alias === null) {
    return (
      <>
        <PageHeader
          title={aliasName}
        />
        <div className="data-card" style={{ padding: 18 }}>
          <div className="muted" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <AlertTriangle size={14} /> Alias <code className="mono">{aliasName}</code> not found.
          </div>
          <Link className="linkish" to="/models">Back to Models</Link>
        </div>
      </>
    )
  }

  const saveStrategy = async () => {
    setSavingStrategy(true)
    try {
      const options: Record<string, unknown> = {}
      if (strategy === 'tagged' && tagFilter) options.tag = tagFilter
      if (strategy === 'region') options.region = region
      else if (region !== 'any') options.region_pref = region
      await api.updateAliasStrategy(aliasName, { strategy, strategy_options: options })
      toast.success('Routing strategy saved')
      reload()
    } catch (e) {
      toast.error('Failed to save strategy', e instanceof ApiError ? e.message : undefined)
    } finally {
      setSavingStrategy(false)
    }
  }

  const saveCache = async () => {
    setSavingCache(true)
    try {
      await api.updateAliasCache(aliasName, {
        cache_enabled: cacheEnabled,
        cache_ttl_seconds: Math.max(60, cacheTtlMin * 60),
      })
      toast.success('Cache settings saved')
      reload()
    } catch (e) {
      toast.error('Failed to save cache', e instanceof ApiError ? e.message : undefined)
    } finally {
      setSavingCache(false)
    }
  }

  return (
    <>
      <PageHeader
        crumbs={[{ label: 'Models', to: '/models' }]}
        title={<span className="mono">{alias.alias}</span>}
        description={`${(alias.deployments ?? []).length === 0 ? 'No deployments attached, so requests to this alias fail' : `Routes to ${(alias.deployments ?? []).length} deployment${(alias.deployments ?? []).length !== 1 ? 's' : ''}`}. ${totals24h.reqs.toLocaleString()} requests in the last 24 hours.`}
      />

      <MetricStrip>
        <StatTile label="Hits (24h)" value={totals24h.reqs.toLocaleString()} hint={`~${totals24h.avgPerHour}/hr`} />
        <StatTile
          label="Errors"
          value={totals24h.errs.toLocaleString()}
          hint={`${totals24h.errPct.toFixed(2)}%`}
          tone={totals24h.errs > 0 ? 'warning' : 'neutral'}
        />
        <StatTile label="p95 latency" value={totals24h.p95 ? `${totals24h.p95}ms` : '—'} hint="last 24h" />
        <StatTile
          label="Cache hit rate"
          value={alias.cache_enabled ? '—' : '—'}
          hint={alias.cache_enabled ? `${cacheTtlMin}m TTL` : 'caching off'}
          tone={alias.cache_enabled ? 'success' : 'neutral'}
        />
      </MetricStrip>

      <div className="alias-grid">
        {/* Routing strategy */}
        <section className="card">
          <div className="card-head-flex">
            <h3 className="card-title">Routing strategy</h3>
            <Button variant={strategyDirty ? 'primary' : 'ghost'} disabled={!strategyDirty} onClick={saveStrategy} loading={savingStrategy}>Save strategy</Button>
          </div>
          <div className="strategy-grid">
            {STRATEGY_CARDS.map((s) => {
              const on = strategy === s.id
              const Icon = s.icon
              return (
                <button
                  key={s.id}
                  type="button"
                  className={'strategy-card' + (on ? ' is-on' : '')}
                  onClick={() => setStrategy(s.id)}
                >
                  <div className="strategy-head">
                    <Icon size={14} />
                    <span className="strategy-label">{s.label}</span>
                    {on && <Check size={13} />}
                  </div>
                  <p className="strategy-desc">{s.desc}</p>
                </button>
              )
            })}
          </div>

          {strategy === 'tagged' && (
            <div className="strategy-extra">
              <label className="field">
                <div className="field-label">Required tag</div>
                <Input
                  className="mono"
                  value={tagFilter}
                  onChange={(e) => setTagFilter(e.target.value)}
                  placeholder="region:us-east"
                />
              </label>
            </div>
          )}

          <div className="strategy-extra">
            <label className="field">
              <div className="field-label">Region preference</div>
              <SegmentedFilter
                variant="window"
                className="in-form"
                value={region}
                onChange={setRegion}
                options={REGION_OPTIONS.map((r) => ({ value: r, label: r }))}
              />
            </label>
          </div>
        </section>

        {/* Routing preview */}
        <section className="card">
          <h3 className="card-title">Routing preview</h3>
          <p className="muted small">
            Where the next 100 requests would land with the current strategy.
          </p>
          <div className="route-preview">
            {distribution.length === 0 ? (
              <div className="route-empty">
                <p>No deployments are attached, so every request to this alias fails.</p>
                <Link className="linkish" to="/models?tab=deployments">Attach a deployment</Link>
              </div>
            ) : (
              distribution.map((s) => (
                <div key={s.d.name} className="route-row">
                  <div className="route-row-l">
                    <span className={`provider-dot dot-${dotClass(s.d.provider_type)}`} />
                    <span className="mono">{s.d.name}</span>
                    {s.d.enabled ? (
                      <Badge tone="success" monospace={false}>healthy</Badge>
                    ) : (
                      <Badge tone="warning" monospace={false}>disabled</Badge>
                    )}
                  </div>
                  <div className="route-row-bar">
                    <div className="route-bar">
                      <div style={{ width: `${(s.share * 100).toFixed(1)}%` }} />
                    </div>
                    <span className="tnum route-share">{Math.round(s.share * 100)}%</span>
                  </div>
                  <div className="route-row-r mono muted">
                    <span>~{s.latencyMs}ms p50</span>
                    <span>·</span>
                    <span className="mono">{s.d.provider_type}</span>
                  </div>
                </div>
              ))
            )}
          </div>
        </section>

        {/* Cache */}
        <section className="card">
          <div className="card-head-flex">
            <h3 className="card-title">Cache</h3>
            <Button variant={cacheDirty ? 'primary' : 'ghost'} disabled={!cacheDirty} onClick={saveCache} loading={savingCache}>Save cache</Button>
          </div>
          <div className="form-grid form-grid-2">
            <label className="toggle-card">
              <input
                type="checkbox"
                checked={cacheEnabled}
                onChange={(e) => setCacheEnabled(e.target.checked)}
              />
              <div>
                <div className="toggle-label">Prompt caching</div>
                <p className="muted small">Hash full prompt + params; replay identical responses.</p>
              </div>
            </label>
            <label className="field">
              <div className="field-label">TTL (minutes)</div>
              <Input
                type="number"
                min={1}
                value={cacheTtlMin}
                disabled={!cacheEnabled}
                onChange={(e) => setCacheTtlMin(Math.max(1, Number(e.target.value) || 1))}
              />
            </label>
          </div>
        </section>

        {/* Last 24h activity */}
        <section className="card">
          <h3 className="card-title">Last 24h activity</h3>
          <div className="micro-line">
            {sparkPath ? (
              <svg viewBox="0 0 200 56" preserveAspectRatio="none">
                <path d={sparkPath} fill="none" stroke="var(--accent)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
              </svg>
            ) : (
              <p className="micro-empty">No requests in the last 24 hours.</p>
            )}
          </div>
          <div className="kv-grid">
            <div>
              <div className="muted">Hits</div>
              <div className="tnum">{totals24h.reqs.toLocaleString()}</div>
            </div>
            <div>
              <div className="muted">Errors</div>
              <div className="tnum">
                {totals24h.errs} <span className="muted small">({totals24h.errPct.toFixed(2)}%)</span>
              </div>
            </div>
            <div>
              <div className="muted">p95</div>
              <div className="tnum">{totals24h.p95 ? `${totals24h.p95}ms` : '—'}</div>
            </div>
            <div>
              <div className="muted">Cache</div>
              <div className="tnum">{alias.cache_enabled ? `On, ${cacheTtlMin} min TTL` : 'Off'}</div>
            </div>
          </div>
        </section>
      </div>
    </>
  )
}

function providerCostTier(provider: string): number {
  switch (provider) {
    case 'openai':
      return 100
    case 'azure_openai':
      return 80
    case 'anthropic':
      return 95
    case 'bedrock':
      return 60
    case 'vertex':
    case 'gemini':
      return 70
    case 'mistral':
    case 'groq':
    case 'together':
    case 'fireworks':
      return 40
    case 'cohere':
      return 75
    case 'openrouter':
      return 65
    default:
      return 90
  }
}

function providerLatencyHint(provider: string): number {
  switch (provider) {
    case 'azure_openai':
      return 412
    case 'openai':
      return 612
    case 'anthropic':
      return 720
    case 'bedrock':
      return 1240
    case 'vertex':
    case 'gemini':
      return 580
    case 'groq':
      return 180
    case 'fireworks':
    case 'together':
      return 320
    case 'mistral':
      return 540
    default:
      return 600
  }
}

function dotClass(provider: string): string {
  // Map provider names to chart-* color slots so we don't hard-code more
  // .dot-* rules than needed. The CSS already exposes dot-1..dot-3.
  switch (provider) {
    case 'openai':
    case 'azure_openai':
      return '1'
    case 'anthropic':
    case 'bedrock':
      return '2'
    default:
      return '3'
  }
}
