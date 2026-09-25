import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router-dom'
import {
  Bell,
  Plus,
  RefreshCcw,
  Search,
  Server,
  Shield,
  Trash2,
  X,
} from 'lucide-react'
import { api, ApiError } from '../api/client'
import { useQuery } from '../lib/useQuery'
import { fmtDateTime } from '../lib/format'
import type {
  AlertEvent,
  AlertRule,
  AuditEvent,
  Info,
  Passthrough,
  ProviderHealth,
  Team,
} from '../types'
import {
  Badge,
  Button,
  Drawer,
  EmptyRow,
  ErrorState,
  Field,
  Input,
  MetricStrip,
  Modal,
  PageHeader,
  SearchSelect,
  SegmentedFilter,
  Select,
  StatTile,
  Table,
  Td,
  Tr,
  useConfirm,
  useToast,
} from '../components/ui'
import { Pagination } from '../components/Pagination'
import RoutingConcurrencyPanel from '../components/RoutingConcurrencyPanel'
import GuardrailsPanel from '../components/GuardrailsPanel'

export type OperationsSection = 'runtime' | 'providers' | 'guardrails' | 'alerts' | 'passthroughs' | 'audit' | 'concurrency'

const sectionDetails: Record<OperationsSection, { title: string; group: string; description: string }> = {
  runtime: { title: 'Gateway settings', group: 'System', description: 'Instance information and connection details.' },
  providers: { title: 'Provider health', group: 'Gateway', description: 'Availability and circuit state per upstream deployment.' },
  guardrails: { title: 'Guardrails', group: 'Safety & limits', description: 'Text policies and their enforcement activity.' },
  alerts: { title: 'Alerts', group: 'Observability', description: 'Notification rules and recent alert events.' },
  passthroughs: { title: 'Passthrough routes', group: 'Gateway', description: 'Authenticated routes to upstream APIs.' },
  audit: { title: 'Audit log', group: 'Observability', description: 'Who changed what, and when.' },
  concurrency: { title: 'Limits', group: 'Policies', description: 'Gateway-wide concurrency caps per model alias and provider type.' },
}

const legacySections: Record<string, string> = { providers: '/providers', guardrails: '/guardrails', alerts: '/alerts', passthroughs: '/passthroughs', audit: '/audit' }

// The operational sub-areas left the sidebar (which now shows one
// Settings entry); this strip is how you move between them.
// Guardrails, limits, alerts and the audit log are their own sidebar
// entries; these tabs cover only the gateway's system configuration.
const sectionLinks: { to: string; id: OperationsSection; label: string }[] = [
  { to: '/settings', id: 'runtime', label: 'Runtime' },
  { to: '/providers', id: 'providers', label: 'Providers' },
  { to: '/passthroughs', id: 'passthroughs', label: 'Passthroughs' },
]

export default function Settings({ section = 'runtime' }: { section?: OperationsSection }) {
  const [params] = useSearchParams()
  const [refreshKey, setRefreshKey] = useState(0)
  const needsRuntime = section === 'runtime' || section === 'providers'

  // Core runtime load (instance info + provider health), polled every 10s
  // while a runtime-backed section is shown. useQuery skips hidden tabs
  // and in-flight ticks, and the deps no longer include toast, so the
  // interval isn't torn down and rebuilt on unrelated renders.
  const core = useQuery(
    () =>
      Promise.all([api.getInfo(), api.listProviderHealth()]).then(([info, health]) => ({
        info,
        health,
      })),
    [needsRuntime],
    { pollMs: 10_000, enabled: needsRuntime },
  )
  const info = core.data?.info ?? null
  const providerHealth = core.data?.health ?? null

  const legacy = section === 'runtime' ? legacySections[params.get('tab') || ''] : undefined
  if (legacy) return <Navigate to={legacy} replace />
  const details = sectionDetails[section]
  return (
    <>
      <PageHeader title={details.title} description={details.description}
        actions={<Button variant="ghost" leadingIcon={<RefreshCcw size={13} />} loading={core.loading || core.refreshing}
          onClick={() => { if (needsRuntime) core.reload(); else setRefreshKey((value) => value + 1) }}>Refresh</Button>} />
      {sectionLinks.some((l) => l.id === section) && <nav className="section-tabs" aria-label="Settings sections">
        {sectionLinks.map((tab) => (
          <Link key={tab.id} to={tab.to} aria-current={section === tab.id ? 'page' : undefined}
            className={'section-tab' + (section === tab.id ? ' is-on' : '')}>
            {tab.label}
          </Link>
        ))}
      </nav>}
      {section === 'runtime' && <div className="scope-notice">
        <Server size={18} /><div><strong>Instance configuration · read-only</strong><p>Server settings are managed in your deployment configuration. Use the setup wizard to connect models and issue your first key.</p></div>
        <Link className="btn btn-ghost" to="/setup">Open setup wizard</Link>
      </div>}
      {section === 'concurrency' && <div className="scope-notice"><Shield size={18} /><div><strong>Shared across every team</strong><p>To cap a single team, key or customer, open that team's Budget & limits section.</p></div><Link className="linkish" to="/teams">View teams</Link></div>}
      <div className="page-section" key={section + refreshKey}>
        {section === 'runtime' && (core.error && core.data === null ? (
          <ErrorState
            title="Couldn't load gateway settings"
            message={core.error.message}
            onRetry={core.reload}
            retrying={core.refreshing}
          />
        ) : (
          <RuntimeTab info={info} providerHealth={providerHealth} />
        ))}
        {section === 'providers' && (core.error && core.data === null ? (
          <ErrorState
            title="Couldn't load provider health"
            message={core.error.message}
            onRetry={core.reload}
            retrying={core.refreshing}
          />
        ) : (
          <ProvidersTab health={providerHealth} />
        ))}
        {section === 'guardrails' && <GuardrailsTab />}
        {section === 'alerts' && <AlertsTab />}
        {section === 'passthroughs' && <PassthroughsTab />}
        {section === 'audit' && <AuditTab />}
        {section === 'concurrency' && <RoutingConcurrencyPanel />}
      </div>
    </>
  )
}

// ─── Runtime tab ─────────────────────────────────────────────────────────
function RuntimeTab({
  info,
  providerHealth,
}: {
  info: Info | null
  providerHealth: ProviderHealth[] | null
}) {
  const healthyCount = (providerHealth ?? []).filter((p) => p.ready).length
  const totalProviders = (providerHealth ?? []).length

  return (
    <>
      <MetricStrip>
        <StatTile
          label="Version"
          value={<span className="mono text-base">{info?.version ?? '—'}</span>}
        />
        <StatTile label="Uptime" value={info ? formatUptime(info.uptime_seconds ?? 0) : '—'} hint="since boot" />
        <StatTile
          label="Inflight requests"
          value={(info?.inflight ?? 0).toLocaleString()}
          hint="admin chain only"
        />
        <StatTile
          label="Healthy providers"
          value={healthyCount}
          hint={`of ${totalProviders}`}
          tone={
            totalProviders > 0 && healthyCount === totalProviders
              ? 'success'
              : healthyCount === 0
              ? 'danger'
              : 'warning'
          }
        />
        <StatTile label="Active keys" value={info?.counts.active_keys ?? 0} />
        <StatTile label="Pending invites" value={info?.counts.pending_invites ?? 0} hint="awaiting accept" />
      </MetricStrip>

      <div className="alias-grid">
        <section className="card">
          <h3 className="card-title">Configuration</h3>
          <div className="kv-list">
            <KvRow label="Listen" value={info?.listen_addr || ':4000'} mono />
            <KvRow label="Database" value={info?.db_ok ? 'connected' : 'down'} tone={info?.db_ok ? 'ok' : 'err'} />
            <KvRow label="Callback bus" value={!info ? 'unknown' : info.redis_ok ? 'enabled' : 'unconfigured'} tone={info?.redis_ok ? 'ok' : 'neutral'} />
            <KvRow
              label="Master key"
              value={info?.master_key_suffix ? `sk-master ··· ${info.master_key_suffix}` : 'not set'}
              mono
            />
            <KvRow
              label="Last config change"
              value={
                info?.last_config_change
                  ? `${info.last_config_change.action} · ${formatRelative(info.last_config_change.when)}`
                  : '—'
              }
            />
            <KvRow label="Aliases" value={(info?.aliases.length ?? 0).toString()} />
            <KvRow label="Deployments" value={(info?.deployments.length ?? 0).toString()} />
          </div>
        </section>

        <section className="card">
          <h3 className="card-title">Connection details</h3>
          <p className="muted small">
            Point client SDKs at this gateway's <code className="mono">/v1</code> endpoint with a virtual key issued
            from a team page.
          </p>
          <div className="kv-list">
            <KvRow label="OpenAI base URL" value={`${origin()}/v1`} mono copy />
            <KvRow label="Health" value={`${origin()}/healthz`} mono copy />
            <KvRow label="Metrics" value={`${origin()}/metrics`} mono copy />
          </div>
          <div style={{ marginTop: 12 }}>
            <Link className="linkish" to="/teams">
              Issue a key
            </Link>
          </div>
        </section>
      </div>
    </>
  )
}

function KvRow({
  label,
  value,
  mono,
  tone,
  copy,
}: {
  label: string
  value: string
  mono?: boolean
  tone?: 'ok' | 'err' | 'neutral'
  copy?: boolean
}) {
  const toast = useToast()
  return (
    <div className="kv-row">
      <span className="muted">{label}</span>
      <span className={mono ? 'mono' : ''}>
        {tone ? <span className={`pill pill-${tone} tnum`}>{value}</span> : value}
        {copy && (
          <button
            className="linkish"
            style={{ marginLeft: 6, fontSize: 11 }}
            onClick={() => {
              navigator.clipboard?.writeText(value)
              toast.success(`Copied ${label}`)
            }}
          >
            copy
          </button>
        )}
      </span>
    </div>
  )
}

// ─── Providers tab ───────────────────────────────────────────────────────
function ProvidersTab({ health }: { health: ProviderHealth[] | null }) {
  // A dev or test instance easily accumulates dozens of deployments; the
  // ready/not-ready filter keeps the grid scannable at that scale.
  const [filter, setFilter] = useState<'all' | 'ready' | 'down'>('all')
  if (health === null) {
    return <div className="muted" style={{ padding: 20 }}>Loading…</div>
  }
  if (health.length === 0) {
    return (
      <div className="data-card" style={{ padding: 24 }}>
        <div className="muted">No deployments registered. Add one in Models → Deployments.</div>
      </div>
    )
  }
  const ready = health.filter((p) => p.ready).length
  const shown = health.filter((p) => filter === 'all' || (filter === 'ready') === p.ready)
  return (
    <>
      <div className="filter-bar">
        <SegmentedFilter
          value={filter}
          onChange={setFilter}
          options={[
            { value: 'all', label: 'All', count: health.length },
            { value: 'ready', label: 'Ready', count: ready, tone: 'ok' },
            { value: 'down', label: 'Not ready', count: health.length - ready, tone: 'warn' },
          ]}
        />
        <span className="muted">
          Health checks every 10s. RPM and error % are computed from the last 5 minutes of /v1 traffic.
        </span>
      </div>
      <Table head={['Deployment', 'Provider', 'Status', <span className="cell-num" key="p50">Latency p50</span>, <span className="cell-num" key="rpm">RPM</span>, <span className="cell-num" key="err">Errors (5m)</span>, 'Last failure']}>
        {shown.length === 0 ? (
          <EmptyRow cols={7}>No deployments in this state.</EmptyRow>
        ) : (
          shown.map((p) => (
            <Tr key={p.name}>
              <Td mono>{p.name}</Td>
              <Td><span className="muted mono">{p.provider_type}</span></Td>
              <Td>
                {!p.enabled ? <Badge tone="neutral" monospace={false}>Disabled</Badge>
                  : p.circuit === 'open' ? <Badge tone="danger" monospace={false}>Circuit open</Badge>
                  : p.ready ? <Badge tone="success" monospace={false}>Ready</Badge>
                  : <Badge tone="warning" monospace={false}>Unavailable</Badge>}
              </Td>
              <Td num className="tnum">{p.stats?.p50_ms ? `${Math.round(p.stats.p50_ms)} ms` : <span className="muted">—</span>}</Td>
              <Td num className="tnum">{(p.stats?.rpm ?? 0).toFixed(1)}</Td>
              <Td num className={'tnum' + ((p.stats?.error_pct ?? 0) > 1 ? ' lat-warn' : '')}>{(p.stats?.error_pct ?? 0).toFixed(1)}%</Td>
              <Td>
                {p.consecutive_failures > 0
                  ? <span title={p.last_error || undefined}>{p.consecutive_failures} in a row{p.last_error ? `: ${p.last_error}` : ''}</span>
                  : <span className="muted">None</span>}
              </Td>
            </Tr>
          ))
        )}
      </Table>
    </>
  )
}


// ─── Guardrails tab ──────────────────────────────────────────────────────
function GuardrailsTab() { return <GuardrailsPanel /> }

// ─── Alerts tab ──────────────────────────────────────────────────────────
function AlertsTab() {
  const [rules, setRules] = useState<AlertRule[] | null>(null)
  const [events, setEvents] = useState<AlertEvent[] | null>(null)
  const [showRule, setShowRule] = useState(false)
  const toast = useToast()
  const confirm = useConfirm()

  const reload = useCallback(async () => {
    const results = await Promise.allSettled([api.listAlertRules(), api.listAlertEvents(20)])
    if (results[0].status === 'fulfilled') setRules(results[0].value)
    if (results[1].status === 'fulfilled') setEvents(results[1].value)
  }, [])

  useEffect(() => {
    reload()
  }, [reload])

  const toggleRule = async (rule: AlertRule) => {
    try {
      await api.updateAlertRule(rule.id, { enabled: !rule.enabled })
      toast.success(`${rule.name} ${rule.enabled ? 'disabled' : 'enabled'}`)
      reload()
    } catch (e) {
      toast.error('Failed to update rule', e instanceof ApiError ? e.message : undefined)
    }
  }

  const deleteRule = async (rule: AlertRule) => {
    const ok = await confirm({
      title: `Delete alert "${rule.name}"?`,
      description: 'Past events stay in the log; future evaluations stop.',
      confirmLabel: 'Delete',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.deleteAlertRule(rule.id)
      toast.success('Rule deleted')
      reload()
    } catch (e) {
      toast.error('Failed to delete rule', e instanceof ApiError ? e.message : undefined)
    }
  }

  return (
    <>
      <div className="filter-bar">
        <span className="muted">
          Rules are checked every 30 seconds and notify Slack or a webhook when their condition holds.
        </span>
        <div className="filter-cluster" style={{ marginLeft: 'auto' }}>
          <Button leadingIcon={<Plus size={13} />} onClick={() => setShowRule(true)}>
            New alert rule
          </Button>
        </div>
      </div>

      <div className="alias-grid">
        <Table
          head={['Rule', 'Trigger', 'Channel', 'Last fired', '']}
          header={
            <div className="data-head">
              <h2 className="data-h">
                Rules <span className="data-count tnum">{(rules ?? []).length}</span>
              </h2>
            </div>
          }
        >
          {rules === null ? (
            <SkeletonRows cols={5} />
          ) : rules.length === 0 ? (
            <EmptyRow cols={5}>
              <Bell size={18} />
              <span>No alert rules yet.</span>
            </EmptyRow>
          ) : (
            rules.map((r) => {
              const lastFired = events?.find((e) => e.rule_id === r.id)?.fired_at
              return (
                <Tr key={r.id}>
                  <Td>{r.name}</Td>
                  <Td className="muted">{formatTrigger(r)}</Td>
                  <Td className="muted">
                    <span className="capitalize">{r.channel_type}</span>
                  </Td>
                  <Td className="muted">{lastFired ? formatRelative(lastFired) : '—'}</Td>
                  <Td align="right">
                    <div className="row-actions">
                      <button
                        className={'switch' + (r.enabled ? ' is-on' : '')}
                        onClick={() => toggleRule(r)}
                        aria-label={r.enabled ? 'Disable rule' : 'Enable rule'}
                      >
                        <span className="switch-track" />
                      </button>
                      <button className="linkish" style={{ color: 'var(--danger-fg)' }} onClick={() => deleteRule(r)}>
                        <Trash2 size={12} />
                      </button>
                    </div>
                  </Td>
                </Tr>
              )
            })
          )}
        </Table>

        <section className="card">
          <h3 className="card-title">Recent events</h3>
          <p className="muted small">The last 20 firings, newest first.</p>
          <div className="event-list">
            {events === null ? (
              <div className="muted" style={{ padding: 12 }}>Loading…</div>
            ) : events.length === 0 ? (
              <div className="muted" style={{ padding: 12 }}>No alerts have fired in the recent window.</div>
            ) : (
              events.map((e) => (
                <div key={e.id} className="event-row">
                  <Bell
                    size={13}
                    className={e.delivery_status === 'failed' ? 'event-icon tone-err' : 'event-icon tone-warn'}
                  />
                  <div className="event-body">
                    <div className="event-rule">{e.rule_name}</div>
                    <div className="event-detail muted small">
                      {e.delivery_status}
                      {e.delivery_error ? ` · ${e.delivery_error}` : ''}
                    </div>
                  </div>
                  <div className="event-t muted small">{formatRelative(e.fired_at)}</div>
                </div>
              ))
            )}
          </div>
        </section>
      </div>

      {showRule && <RuleBuilderModal onClose={() => setShowRule(false)} onCreated={reload} />}
    </>
  )
}

type Trigger = 'budget_threshold' | 'budget_exceeded' | 'error_rate' | 'latency_p95' | 'provider_unavailable'

const TRIGGERS: { value: Trigger; label: string; hint: string }[] = [
  { value: 'budget_threshold', label: 'Spend reaches a share of budget', hint: 'Checked against the team’s budget period.' },
  { value: 'budget_exceeded', label: 'Spend exceeds budget', hint: 'Fires once the team is at or over 100% of its limit.' },
  { value: 'error_rate', label: 'Server error rate is high', hint: 'Share of requests that ended in a 5xx over the window.' },
  { value: 'latency_p95', label: 'p95 latency is slow', hint: '95th-percentile request latency over the window.' },
  { value: 'provider_unavailable', label: 'A deployment is down', hint: 'Every health check in the window failed or the circuit stayed open.' },
]

const CHANNEL_HINT: Record<string, { placeholder: string; hint: string }> = {
  slack: { placeholder: 'https://hooks.slack.com/services/…', hint: 'A Slack incoming-webhook URL.' },
  webhook: { placeholder: 'https://example.com/gatemux-alerts', hint: 'Receives a JSON POST with the rule’s measurements.' },
}

function RuleBuilderModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [trigger, setTrigger] = useState<Trigger>('budget_threshold')
  const [team, setTeam] = useState<Team | null>(null)
  const [thresholdPct, setThresholdPct] = useState('80')
  const [errorPct, setErrorPct] = useState('5')
  const [latencyMs, setLatencyMs] = useState('5000')
  const [windowMin, setWindowMin] = useState('15')
  const [minRequests, setMinRequests] = useState('20')
  const [channelType, setChannelType] = useState('slack')
  const [channelTarget, setChannelTarget] = useState('')
  const [cooldownMin, setCooldownMin] = useState('15')
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const isBudget = trigger === 'budget_threshold' || trigger === 'budget_exceeded'
  const isTraffic = trigger === 'error_rate' || trigger === 'latency_p95'
  const current = TRIGGERS.find((t) => t.value === trigger)!

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (isBudget && !team) {
      toast.error('Choose a team', 'Budget alerts watch one team’s budget.')
      return
    }
    setBusy(true)
    try {
      const opts: Record<string, number> = {}
      if (trigger === 'budget_threshold') opts.threshold_pct = Number(thresholdPct) || 80
      if (isTraffic) {
        opts.threshold = Number(trigger === 'error_rate' ? errorPct : latencyMs)
        opts.window_minutes = Number(windowMin) || 15
        opts.min_requests = Number(minRequests) || 0
      }
      if (trigger === 'provider_unavailable') opts.window_minutes = Number(windowMin) || 5
      await api.createAlertRule({
        name,
        scope_type: team ? 'team' : 'global',
        scope_id: team ? team.id : null,
        trigger_type: trigger,
        threshold_options: opts,
        channel_type: channelType,
        channel_target: channelTarget,
        cooldown_seconds: Math.max(1, Number(cooldownMin) || 15) * 60,
      })
      toast.success('Alert rule created')
      onCreated()
      onClose()
    } catch (e) {
      toast.error('Couldn’t create the rule', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal onClose={onClose} title="New alert rule" size="md">
      <form onSubmit={submit} className="space-y-5">
        <div className="form-grid">
          <Field label="Name" required>
            <Input required value={name} onChange={(e) => setName(e.target.value)} placeholder="Platform team near budget" />
          </Field>
          <Field label="Alert when" hint={current.hint}>
            <Select value={trigger} onChange={(e) => { setTrigger(e.target.value as Trigger); if (e.target.value === 'provider_unavailable') setWindowMin('5') }}>
              {TRIGGERS.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
            </Select>
          </Field>
          {trigger !== 'provider_unavailable' && (
            <Field label="Team" required={isBudget} hint={isBudget ? undefined : 'Leave empty to watch all traffic.'}>
              <SearchSelect<Team>
                value={team}
                onChange={setTeam}
                search={(q) => api.listTeams({ limit: 20 }, q).then((p) => p.items)}
                label={(t) => t.name}
                detail={(t) => t.slug}
                emptyChoice={isBudget ? 'Choose a team' : 'All teams'}
                placeholder="Search teams…"
              />
            </Field>
          )}
          <div className="form-grid form-grid-2">
            {trigger === 'budget_threshold' && (
              <Field label="Share of budget (%)">
                <Input type="number" min="1" max="100" value={thresholdPct} onChange={(e) => setThresholdPct(e.target.value)} />
              </Field>
            )}
            {trigger === 'error_rate' && (
              <Field label="Error rate at or above (%)">
                <Input type="number" min="0.1" max="100" step="0.1" value={errorPct} onChange={(e) => setErrorPct(e.target.value)} />
              </Field>
            )}
            {trigger === 'latency_p95' && (
              <Field label="p95 at or above (ms)">
                <Input type="number" min="1" value={latencyMs} onChange={(e) => setLatencyMs(e.target.value)} />
              </Field>
            )}
            {(isTraffic || trigger === 'provider_unavailable') && (
              <Field label="Over the last (minutes)">
                <Input type="number" min="1" max="1440" value={windowMin} onChange={(e) => setWindowMin(e.target.value)} />
              </Field>
            )}
            {isTraffic && (
              <Field label="Minimum requests" hint="Quiet periods below this never fire.">
                <Input type="number" min="0" value={minRequests} onChange={(e) => setMinRequests(e.target.value)} />
              </Field>
            )}
            <Field label="Quiet period (minutes)" hint="After firing, wait this long before firing again.">
              <Input type="number" min="1" value={cooldownMin} onChange={(e) => setCooldownMin(e.target.value)} />
            </Field>
          </div>
          <div className="form-grid form-grid-2">
            <Field label="Send to">
              <Select value={channelType} onChange={(e) => setChannelType(e.target.value)}>
                <option value="slack">Slack</option>
                <option value="webhook">Webhook</option>
              </Select>
            </Field>
            <Field label="URL" required hint={CHANNEL_HINT[channelType].hint}>
              <Input required type="url" value={channelTarget} onChange={(e) => setChannelTarget(e.target.value)} placeholder={CHANNEL_HINT[channelType].placeholder} />
            </Field>
          </div>
        </div>
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose} type="button">Cancel</Button>
          <Button type="submit" loading={busy}>Create rule</Button>
        </div>
      </form>
    </Modal>
  )
}

// ─── Passthroughs tab ────────────────────────────────────────────────────
function PassthroughsTab() {
  const [items, setItems] = useState<Passthrough[] | null>(null)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Passthrough | null>(null)
  const confirm = useConfirm()
  const toast = useToast()

  const reload = useCallback(async () => {
    try {
      const p = await api.listPassthroughs({ limit: 200 })
      setItems(p.items)
    } catch (e) {
      toast.error('Failed to load passthroughs', e instanceof ApiError ? e.message : undefined)
    }
  }, [toast])

  useEffect(() => { reload() }, [reload])

  const toggle = async (p: Passthrough) => {
    try {
      await api.updatePassthrough(p.name, { enabled: !p.enabled })
      toast.success(p.enabled ? 'Passthrough disabled' : 'Passthrough enabled')
      reload()
    } catch (e) {
      toast.error('Update failed', e instanceof ApiError ? e.message : undefined)
    }
  }

  const remove = async (p: Passthrough) => {
    const ok = await confirm({
      title: `Delete passthrough ${p.name}?`,
      description: 'New requests to /passthrough/' + p.name + '/* will return 404. Audit history is retained.',
      confirmLabel: 'Delete',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.deletePassthrough(p.name)
      toast.success('Passthrough deleted')
      reload()
    } catch (e) {
      toast.error('Delete failed', e instanceof ApiError ? e.message : undefined)
    }
  }

  return (
    <>
      <div className="page-toolbar">
        <div className="page-toolbar-l">
          <span className="muted">
            Generic proxies — clients hit <code className="mono">/passthrough/{'{name}'}/*</code> with their GateMux key, and the gateway swaps in the configured upstream credential.
          </span>
        </div>
        <div className="page-toolbar-r">
          <Button leadingIcon={<Plus size={13} />} onClick={() => setCreating(true)}>Add passthrough</Button>
        </div>
      </div>
      <Table
        head={['Name', 'Target URL', 'Auth header', 'Status', '']}
        header={
          <div className="data-head">
            <h2 className="data-h">
              Passthroughs <span className="data-count tnum">{items?.length ?? 0}</span>
            </h2>
          </div>
        }
      >
        {items === null ? (
          <EmptyRow cols={5}>Loading…</EmptyRow>
        ) : items.length === 0 ? (
          <EmptyRow cols={5}>
            <span className="muted">No passthroughs configured. Add one to expose an upstream API surface (e.g. OpenAI Files, Langfuse) behind your GateMux keys.</span>
          </EmptyRow>
        ) : (
          items.map((p) => (
            <Tr key={p.name}>
              <Td mono>{p.name}</Td>
              <Td className="muted mono text-[11px]">{p.target_url}</Td>
              <Td className="muted text-[11px]">
                {p.auth_header ? (
                  <span className="mono">{p.auth_header}: {p.auth_value_prefix || ''}<span className="muted">${'{'}{p.auth_value_env || '—'}{'}'}</span></span>
                ) : (
                  <span className="muted">none</span>
                )}
              </Td>
              <Td>
                {p.enabled ? <Badge tone="success" monospace={false}>enabled</Badge> : <Badge tone="warning" monospace={false}>disabled</Badge>}
              </Td>
              <Td align="right">
                <div className="row-actions">
                  <button className="linkish" onClick={() => setEditing(p)}>Edit</button>
                  <button className="linkish" onClick={() => toggle(p)}>{p.enabled ? 'Disable' : 'Enable'}</button>
                  <button className="linkish" style={{ color: 'var(--danger-fg)' }} onClick={() => remove(p)}>Delete</button>
                </div>
              </Td>
            </Tr>
          ))
        )}
      </Table>

      {creating && (
        <PassthroughModal
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); reload() }}
        />
      )}
      {editing && (
        <PassthroughModal
          existing={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload() }}
        />
      )}
    </>
  )
}

function PassthroughModal({ existing, onClose, onSaved }: { existing?: Passthrough; onClose: () => void; onSaved: () => void }) {
  const editing = existing != null
  const [name, setName] = useState(existing?.name ?? '')
  const [targetURL, setTargetURL] = useState(existing?.target_url ?? '')
  const [authHeader, setAuthHeader] = useState(existing?.auth_header ?? 'Authorization')
  const [authValueEnv, setAuthValueEnv] = useState(existing?.auth_value_env ?? '')
  const [authValuePrefix, setAuthValuePrefix] = useState(existing?.auth_value_prefix ?? 'Bearer ')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const toast = useToast()

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      if (editing) {
        await api.updatePassthrough(existing!.name, {
          target_url: targetURL,
          auth_header: authHeader,
          auth_value_env: authValueEnv,
          auth_value_prefix: authValuePrefix,
          enabled,
        })
        toast.success('Passthrough updated')
      } else {
        await api.createPassthrough({
          name,
          target_url: targetURL,
          auth_header: authHeader,
          auth_value_env: authValueEnv,
          auth_value_prefix: authValuePrefix,
          enabled,
        })
        toast.success('Passthrough created')
      }
      onSaved()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={editing ? `Edit ${existing!.name}` : 'Add passthrough'} onClose={onClose}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label="Name" required hint="URL-safe id, e.g. openai-files">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="openai-files" required disabled={editing} />
        </Field>
        <Field label="Target URL" required hint="Base URL the gateway forwards to">
          <Input value={targetURL} onChange={(e) => setTargetURL(e.target.value)} placeholder="https://api.openai.com" required />
        </Field>
        <Field label="Auth header" hint="Header name to set on the forwarded request. Leave blank for unauthenticated upstreams.">
          <Input value={authHeader} onChange={(e) => setAuthHeader(e.target.value)} placeholder="Authorization" />
        </Field>
        <Field label="Auth value env var" hint="Env var on the gateway that holds the upstream credential">
          <Input value={authValueEnv} onChange={(e) => setAuthValueEnv(e.target.value)} placeholder="OPENAI_API_KEY" />
        </Field>
        <Field label="Auth value prefix" hint='Prepended to env value. Use "Bearer " for OpenAI/Anthropic, blank for X-API-Key style.'>
          <Input value={authValuePrefix} onChange={(e) => setAuthValuePrefix(e.target.value)} placeholder="Bearer " />
        </Field>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled</span>
        </label>
        {err && <p className="text-xs text-danger-fg">{err}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" loading={busy}>{editing ? 'Save' : 'Create'}</Button>
        </div>
      </form>
    </Modal>
  )
}

// ─── Audit tab ───────────────────────────────────────────────────────────
const ACTOR_LABEL: Record<string, string> = { master_key: 'Master key', user: 'User', service_account: 'Service account', system: 'System' }
const humanize = (v: string) => { const t = v.replace(/[_.]/g, ' '); return t.charAt(0).toUpperCase() + t.slice(1) }

function AuditTab() {
  const [audit, setAudit] = useState<AuditEvent[] | null>(null)
  const [auditTotal, setAuditTotal] = useState(0)
  const [auditLimit, setAuditLimit] = useState(50)
  const [auditOffset, setAuditOffset] = useState(0)
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')
  const [resourceType, setResourceType] = useState('')
  const [actorType, setActorType] = useState('')
  const [facets, setFacets] = useState<{ resource_types: string[]; actor_types: string[] }>({ resource_types: [], actor_types: [] })
  const [selected, setSelected] = useState<AuditEvent | null>(null)
  const toast = useToast()

  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(query.trim()), 300)
    return () => window.clearTimeout(t)
  }, [query])

  useEffect(() => {
    setAuditOffset(0)
  }, [debounced, resourceType, actorType])

  useEffect(() => {
    api.getAuditFacets().then(setFacets).catch(() => {})
  }, [])

  useEffect(() => {
    api
      .listAudit({ limit: auditLimit, offset: auditOffset }, { q: debounced || undefined, resource_type: resourceType || undefined, actor_type: actorType || undefined })
      .then((p) => {
        setAudit(p.items)
        setAuditTotal(p.total)
      })
      .catch((e) => toast.error('Couldn’t load the audit log', e instanceof ApiError ? e.message : undefined))
  }, [auditLimit, auditOffset, debounced, resourceType, actorType, toast])

  const filtered = Boolean(debounced || resourceType || actorType)

  return (
    <>
      <div className="filter-bar">
        <label className="search-shell">
          <Search size={14} />
          <input
            aria-label="Search audit log"
            placeholder="Search actor, action, target…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button className="search-clear" onClick={() => setQuery('')} aria-label="Clear">
              <X size={12} />
            </button>
          )}
        </label>
        <label className="select-shell">
          <select aria-label="Resource type" value={resourceType} onChange={(e) => setResourceType(e.target.value)}>
            <option value="">All resources</option>
            {facets.resource_types.map((t) => <option key={t} value={t}>{humanize(t)}</option>)}
          </select>
        </label>
        <label className="select-shell">
          <select aria-label="Actor type" value={actorType} onChange={(e) => setActorType(e.target.value)}>
            <option value="">All actors</option>
            {facets.actor_types.map((t) => <option key={t} value={t}>{ACTOR_LABEL[t] ?? humanize(t)}</option>)}
          </select>
        </label>
        {filtered && <button className="linkish" onClick={() => { setQuery(''); setResourceType(''); setActorType('') }}>Clear filters</button>}
        <div className="filter-cluster" style={{ marginLeft: 'auto' }}>
          <a className="btn btn-ghost" href="/admin/export/audit.csv" download>
            Export CSV
          </a>
        </div>
      </div>

      <Table
        head={['Time', 'Actor', 'Action', 'Resource', 'Details']}
        header={
          <div className="data-head">
            <h2 className="data-h">
              Events <span className="data-count tnum">{auditTotal.toLocaleString()}</span>
            </h2>
            <span className="muted">Append-only, kept for 90 days</span>
          </div>
        }
        footer={
          <Pagination
            total={auditTotal}
            limit={auditLimit}
            offset={auditOffset}
            onChange={(p) => {
              setAuditLimit(p.limit)
              setAuditOffset(p.offset)
            }}
          />
        }
      >
        {audit === null ? (
          <SkeletonRows cols={5} />
        ) : audit.length === 0 ? (
          <EmptyRow cols={5}>{filtered ? 'No events match these filters.' : 'No audit events yet.'}</EmptyRow>
        ) : (
          audit.map((e) => (
            <Tr key={e.id} onClick={() => setSelected(e)} expanded={selected?.id === e.id}>
              <Td className="tnum audit-time">{fmtDateTime(e.created_at)}</Td>
              <Td>{e.actor_type === 'master_key' ? 'Master key' : <span className="mono">{e.actor_id}</span>}</Td>
              <Td className="mono">{e.action}</Td>
              <Td>
                <span className="muted">{e.resource_type.replace(/_/g, ' ')}</span>{' '}
                <span className="mono audit-resource" title={e.resource_id}>{e.resource_id}</span>
              </Td>
              <Td className="muted audit-meta">{metaSummary(e.metadata)}</Td>
            </Tr>
          ))
        )}
      </Table>
      {selected && <AuditDrawer event={selected} onClose={() => setSelected(null)} />}
    </>
  )
}

// metaSummary names the fields an event carries, so the table scans without
// dumping JSON into cells; the drawer has the values.
function metaSummary(metadata: Record<string, unknown>): string {
  const keys = Object.keys(metadata ?? {})
  if (keys.length === 0) return '—'
  const shown = keys.slice(0, 3).map((k) => k.replace(/_/g, ' ')).join(', ')
  return keys.length > 3 ? `${shown} and ${keys.length - 3} more` : shown
}

function AuditDrawer({ event, onClose }: { event: AuditEvent; onClose: () => void }) {
  const toast = useToast()
  const entries = Object.entries(event.metadata ?? {})
  return (
    <Drawer
      title={<span className="mono">{event.action}</span>}
      subtitle={fmtDateTime(event.created_at)}
      onClose={onClose}
      width={560}
      actions={
        <Button variant="ghost" size="sm" onClick={() => { void navigator.clipboard?.writeText(JSON.stringify(event, null, 2)); toast.success('Event copied') }}>
          Copy JSON
        </Button>
      }
    >
      <section className="detail-card">
        <dl className="kv-flat">
          <div><dt>Actor</dt><dd>{ACTOR_LABEL[event.actor_type] ?? humanize(event.actor_type)} <span className="mono muted">{event.actor_type === 'master_key' ? '' : event.actor_id}</span></dd></div>
          <div><dt>Resource</dt><dd>{humanize(event.resource_type)} <span className="mono">{event.resource_id}</span></dd></div>
        </dl>
      </section>
      <section className="detail-card">
        <div className="detail-card-h">Details</div>
        {entries.length === 0 ? (
          <p className="muted">This event carries no details.</p>
        ) : (
          <dl className="audit-fields">
            {entries.map(([k, v]) => (
              <div key={k}>
                <dt>{humanize(k)}</dt>
                <dd>{v !== null && typeof v === 'object'
                  ? <pre>{JSON.stringify(v, null, 2)}</pre>
                  : <span className={typeof v === 'string' ? undefined : 'mono'}>{v === null || v === '' ? <span className="muted">none</span> : String(v)}</span>}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </section>
    </Drawer>
  )
}

// ─── shared bits ─────────────────────────────────────────────────────────
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

function formatUptime(seconds: number): string {
  if (seconds <= 0) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

function formatRelative(when: string): string {
  const ms = Date.now() - new Date(when).getTime()
  if (ms < 0) return new Date(when).toLocaleString()
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 36) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function origin(): string {
  if (typeof window === 'undefined') return 'https://gateway.example'
  return window.location.origin
}

function formatTrigger(r: AlertRule): string {
  const o = (r.threshold_options ?? {}) as Record<string, unknown>
  const w = o.window_minutes != null ? ` over ${o.window_minutes}m` : ''
  switch (r.trigger_type) {
    case 'budget_threshold': return `Spend ≥ ${o.threshold_pct ?? 80}% of budget`
    case 'budget_exceeded': return 'Spend over budget'
    case 'error_rate': return `5xx rate ≥ ${o.threshold ?? 5}%${w}`
    case 'latency_p95': return `p95 ≥ ${o.threshold ?? 5000} ms${w}`
    case 'provider_unavailable': return `Deployment down${w || ' over 5m'}`
  }
  return r.trigger_type
}
