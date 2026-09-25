import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import SectionTabs, { useSectionTab } from '../components/SectionTabs'
import { Boxes, Plus, Search, Trash2 } from 'lucide-react'
import { api, ApiError } from '../api/client'
import type { Deployment, ModelAlias } from '../types'
import { useQuery } from '../lib/useQuery'
import { useOpenFromUrl } from '../lib/useOpenFromUrl'
import { useDebounced } from '../lib/useDebounced'
import CreateDeploymentModal from '../components/CreateDeploymentModal'
import EditDeploymentModal from '../components/EditDeploymentModal'
import CreateAliasModal from '../components/CreateAliasModal'
import RoutingConcurrencyPanel from '../components/RoutingConcurrencyPanel'
import PricingPanel from '../components/PricingPanel'
import {
  Badge,
  Button,
  EmptyRow,
  ErrorRow,
  ErrorState,
  Field,
  Input,
  Modal,
  PageHeader,
  SkeletonRows,
  Table,
  Td,
  Tr,
  RowMenu,
  SegmentedFilter,
  useConfirm,
  useToast,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

type Tab = 'aliases' | 'deployments' | 'capabilities' | 'concurrency' | 'pricing'

export default function Models() {
  const [tab] = useSectionTab<Tab>(['aliases', 'deployments', 'capabilities', 'concurrency', 'pricing'], 'aliases')
  const [depLimit, setDepLimit] = useState(50)
  const [depOffset, setDepOffset] = useState(0)
  const [aliasLimit, setAliasLimit] = useState(50)
  const [aliasOffset, setAliasOffset] = useState(0)
  const [creatingDep, setCreatingDep] = useState(false)
  const [editingDep, setEditingDep] = useState<Deployment | null>(null)
  const [creatingAlias, setCreatingAlias] = useState(false)
  const [cacheEditAlias, setCacheEditAlias] = useState<ModelAlias | null>(null)
  useOpenFromUrl(() => { if (tab === 'deployments') setCreatingDep(true); else setCreatingAlias(true) })
  const [q, setQ] = useState('')
  const [aliasFilter, setAliasFilter] = useState<'all' | 'broken'>('all')
  const navigate = useNavigate()
  const confirm = useConfirm()
  // Searches run on the server so they cover the whole catalog.
  const debouncedQ = useDebounced(q.trim())
  useEffect(() => { setAliasOffset(0); setDepOffset(0) }, [debouncedQ, aliasFilter])
  const toast = useToast()

  const {
    data: depPage,
    error: depError,
    loading: depLoading,
    refreshing: depRefreshing,
    reload: reloadDeployments,
  } = useQuery(() => api.listDeployments({ limit: depLimit, offset: depOffset }, debouncedQ), [depLimit, depOffset, debouncedQ])
  const {
    data: aliasPage,
    error: aliasError,
    loading: aliasLoading,
    refreshing: aliasRefreshing,
    reload: reloadAliases,
  } = useQuery(
    () => api.listAliases({ limit: aliasLimit, offset: aliasOffset }, { q: debouncedQ, unrouted: aliasFilter === 'broken' }),
    [aliasLimit, aliasOffset, debouncedQ, aliasFilter],
  )
  // Count of aliases with no deployments across the whole catalog, for the
  // filter chip — limit 1 because only the total matters.
  const { data: brokenPage, reload: reloadBroken } = useQuery(() => api.listAliases({ limit: 1 }, { unrouted: true }), [])
  // The alias dialog picks from every deployment, not the loaded page.
  const { data: allDeployments } = useQuery(
    () => (creatingAlias ? api.listDeployments({ limit: 500 }).then((p) => p.items) : Promise.resolve(null)),
    [creatingAlias],
  )
  // Unpaged fetch feeding the deployment → aliases "Used by" map; the paged
  // fetch above drives the aliases table, so both are genuinely needed.
  const { data: allAliasPage, reload: reloadAllAliases } = useQuery(() => api.listAliases({ limit: 500 }), [])

  const deployments = depPage?.items ?? null
  const depTotal = depPage?.total ?? 0
  const aliases = aliasPage?.items ?? null
  const aliasTotal = aliasPage?.total ?? 0
  const allAliases = useMemo(() => allAliasPage?.items ?? [], [allAliasPage])

  const reload = () => {
    reloadDeployments()
    reloadAliases()
    reloadAllAliases()
    reloadBroken()
  }

  const aliasesByDeployment = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const a of allAliases) {
      for (const d of a.deployments ?? []) {
        const list = map.get(d) ?? []
        list.push(a.alias)
        map.set(d, list)
      }
    }
    return map
  }, [allAliases])

  const deleteDeployment = async (name: string) => {
    const used = aliasesByDeployment.get(name) ?? []
    const ok = await confirm({
      title: `Delete deployment "${name}"?`,
      description:
        used.length > 0
          ? `Aliases pointing here will be unrouted: ${used.join(', ')}.`
          : 'This deployment is not referenced by any alias.',
      confirmLabel: 'Delete',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.deleteDeployment(name)
      toast.success(`Deployment "${name}" deleted`)
      reload()
    } catch (e) {
      toast.error('Delete failed', e instanceof ApiError ? e.message : undefined)
    }
  }

  // Sends one tiny real request through the deployment and reports the
  // outcome; the server classifies failures without echoing provider text.
  const testConnection = async (name: string) => {
    toast.info(`Testing ${name}…`, 'Sending one small request upstream.')
    try {
      const r = await api.testDeployment(name)
      if (r.ok) toast.success(`${name}: connection works`, `${r.message} (${r.latency_ms} ms)`)
      else toast.error(`${name}: connection failed`, r.message)
    } catch (e) {
      toast.error(`${name}: test could not run`, e instanceof ApiError ? e.message : undefined)
    }
  }

  const deleteAlias = async (alias: string) => {
    const ok = await confirm({
      title: `Delete alias "${alias}"?`,
      description: 'Clients calling this model name will start receiving 404 responses.',
      confirmLabel: 'Delete',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.deleteAlias(alias)
      toast.success(`Alias "${alias}" deleted`)
      reload()
    } catch (e) {
      toast.error('Delete failed', e instanceof ApiError ? e.message : undefined)
    }
  }

  const shownAliases = aliases ?? []
  const brokenTotal = brokenPage?.total ?? 0
  const filteredDeps = deployments ?? []

  return (
    <>
      <PageHeader
        title="Models"
        description="Aliases clients call, and the deployments behind them."
        actions={
          <>
            {tab === 'aliases' && (
              <Button
                leadingIcon={<Plus size={13} />}
                onClick={() => setCreatingAlias(true)}
                disabled={depTotal === 0 && !debouncedQ}
              >
                Add alias
              </Button>
            )}
            {tab === 'deployments' && (
              <Button leadingIcon={<Plus size={13} />} onClick={() => setCreatingDep(true)}>Add deployment</Button>
            )}
          </>
        }
      />

      <SectionTabs label="Model sections" current={tab} items={[
        { id: 'aliases', label: 'Aliases', count: aliasTotal },
        { id: 'deployments', label: 'Deployments', count: depTotal },
        { id: 'capabilities', label: 'Capabilities' },
        { id: 'concurrency', label: 'Concurrency' },
        { id: 'pricing', label: 'Pricing' },
      ]} />
      {tab === 'pricing' && <PricingPanel />}
      {tab === 'concurrency' && <div className="scope-notice"><div><strong>Gateway-wide routing policy</strong><p>These limits are shared across teams. You can also find them under Safety & limits.</p></div><Link className="linkish" to="/concurrency">Open concurrency workspace</Link></div>}

      {tab !== 'concurrency' && tab !== 'pricing' && <div className="page-toolbar">
        <div className="page-toolbar-l">
          <label className="search-shell">
            <Search size={13} />
            <input
              placeholder={
                tab === 'aliases'
                  ? 'Search aliases…'
                  : tab === 'deployments'
                    ? 'Search deployments…'
                    : 'Search capabilities…'
              }
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
          </label>
          {tab === 'aliases' && (
            <SegmentedFilter
              value={aliasFilter}
              onChange={setAliasFilter}
              options={[
                { value: 'all', label: 'All' },
                { value: 'broken', label: 'No deployments', count: brokenTotal, tone: brokenTotal > 0 ? 'err' : undefined },
              ]}
            />
          )}
        </div>
      </div>}

      {tab === 'capabilities' &&
        (depError && deployments === null ? (
          <ErrorState
            title="Couldn't load deployments"
            message={depError.message}
            onRetry={reloadDeployments}
            retrying={depRefreshing}
          />
        ) : (
          <CapabilityMatrix deployments={deployments} />
        ))}
      {tab === 'concurrency' && <RoutingConcurrencyPanel />}

      {tab === 'aliases' && (
        <Table
          head={['Alias', 'Status', 'Routes to', 'Cache', '']}
          footer={(
            <Pagination
              total={aliasTotal}
              limit={aliasLimit}
              offset={aliasOffset}
              onChange={(p) => {
                setAliasLimit(p.limit)
                setAliasOffset(p.offset)
              }}
            />
          )}
        >
          {aliasLoading ? (
            <SkeletonRows rows={6} cols={5} />
          ) : aliasError && aliases === null ? (
            <ErrorRow
              cols={5}
              title="Couldn't load aliases"
              message={aliasError.message}
              onRetry={reloadAliases}
              retrying={aliasRefreshing}
            />
          ) : shownAliases.length === 0 ? (
            <EmptyRow cols={5}>
              <Boxes size={18} />
              <span>{aliasFilter === 'broken' ? 'Every alias routes to at least one deployment.' : 'No aliases yet. Add a deployment, then an alias that points to it.'}</span>
            </EmptyRow>
          ) : (
            shownAliases.map((a) => {
              const deps = a.deployments ?? []
              return (
                <Tr key={a.alias} onClick={() => navigate(`/models/${encodeURIComponent(a.alias)}`)}>
                  <Td mono>
                    <Link
                      to={`/models/${encodeURIComponent(a.alias)}`}
                      onClick={(e) => e.stopPropagation()}
                      style={{ color: 'inherit', textDecoration: 'none', fontWeight: 500 }}
                    >
                      {a.alias}
                    </Link>
                    {a.managed_by === 'config' && <ConfigBadge />}
                  </Td>
                  <Td>
                    {deps.length === 0
                      ? <Badge tone="danger" monospace={false}>No deployments</Badge>
                      : <Badge tone="success" monospace={false}>Routable</Badge>}
                  </Td>
                  <Td>
                    {deps.length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      <span className="mono muted">
                        {deps[0]}
                        {deps.length > 1 && <span> +{deps.length - 1} fallback{deps.length > 2 ? 's' : ''}</span>}
                      </span>
                    )}
                  </Td>
                  <Td>
                    {a.cache_enabled ? <span>On, {formatTTL(a.cache_ttl_seconds)}</span> : <span className="muted">Off</span>}
                  </Td>
                  <Td align="right">
                    <RowMenu label={`Actions for ${a.alias}`} items={[
                      { label: 'Open', onSelect: () => navigate(`/models/${encodeURIComponent(a.alias)}`) },
                      { label: 'Cache settings…', onSelect: () => setCacheEditAlias(a) },
                      { label: 'Delete alias…', destructive: true, onSelect: () => void deleteAlias(a.alias) },
                    ]} />
                  </Td>
                </Tr>
              )
            })
          )}
        </Table>
      )}

      {tab === 'deployments' && (
        <Table
          head={['Name', 'Provider', 'Upstream model', 'Used by', 'Capabilities', 'Concurrency', 'Status', '']}
          footer={
            <Pagination
              total={depTotal}
              limit={depLimit}
              offset={depOffset}
              onChange={(p) => {
                setDepLimit(p.limit)
                setDepOffset(p.offset)
              }}
            />
          }
        >
          {depLoading ? (
            <SkeletonRows rows={6} cols={8} />
          ) : depError && deployments === null ? (
            <ErrorRow
              cols={8}
              title="Couldn't load deployments"
              message={depError.message}
              onRetry={reloadDeployments}
              retrying={depRefreshing}
            />
          ) : filteredDeps.length === 0 ? (
            <EmptyRow cols={8}>
              <Boxes size={18} />
              <span>No deployments yet.</span>
            </EmptyRow>
          ) : (
            filteredDeps.map((d) => {
              const usedBy = aliasesByDeployment.get(d.name) ?? []
              return (
                <Tr key={d.name}>
                  <Td><span className="mono">{d.name}</span>{d.managed_by === 'config' && <ConfigBadge />}</Td>
                  <Td><span className="mono muted">{d.provider_type}</span></Td>
                  <Td mono>{d.upstream_model}</Td>
                  <Td>
                    {usedBy.length === 0 ? (
                      <span className="muted">Not used</span>
                    ) : (
                      <span className="mono muted" title={usedBy.join(', ')}>
                        {usedBy[0]}{usedBy.length > 1 && ` +${usedBy.length - 1}`}
                      </span>
                    )}
                  </Td>
                  <Td><CapabilityBadges deployment={d} /></Td>
                  <Td className="tnum">{d.max_parallel_requests ? `${d.max_parallel_requests} per process` : <span className="muted">Unlimited</span>}</Td>
                  <Td>
                    {d.has_credential
                      ? <Badge tone="success" monospace={false}>Ready</Badge>
                      : <Badge tone="warning" monospace={false}>No credential</Badge>}
                  </Td>
                  <Td align="right">
                    <div className="row-actions">
                      <button className="linkish" onClick={() => setEditingDep(d)}>Edit</button>
                      <RowMenu label={`Actions for ${d.name}`} items={[
                        { label: 'Test connection', onSelect: () => void testConnection(d.name) },
                        { label: <><Trash2 size={11} /> Delete deployment…</>, destructive: true, onSelect: () => void deleteDeployment(d.name) },
                      ]} />
                    </div>
                  </Td>
                </Tr>
              )
            })
          )}
        </Table>
      )}

      {creatingDep && (
        <CreateDeploymentModal
          onClose={() => setCreatingDep(false)}
          onCreated={() => {
            setCreatingDep(false)
            toast.success('Deployment created')
            reload()
          }}
        />
      )}
      {editingDep && (
        <EditDeploymentModal
          deployment={editingDep}
          onClose={() => setEditingDep(null)}
          onSaved={() => {
            setEditingDep(null)
            toast.success('Deployment updated')
            reload()
          }}
        />
      )}
      {creatingAlias && allDeployments && (
        <CreateAliasModal
          deployments={allDeployments}
          onClose={() => setCreatingAlias(false)}
          onSaved={() => {
            setCreatingAlias(false)
            toast.success('Alias saved')
            reload()
          }}
        />
      )}
      {cacheEditAlias && (
        <AliasCacheModal
          alias={cacheEditAlias}
          onClose={() => setCacheEditAlias(null)}
          onSaved={() => {
            setCacheEditAlias(null)
            toast.success('Cache settings saved')
            reload()
          }}
        />
      )}
    </>
  )
}

function formatTTL(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 86400)}d`
}

function AliasCacheModal({
  alias,
  onClose,
  onSaved,
}: {
  alias: ModelAlias
  onClose: () => void
  onSaved: () => void
}) {
  const [enabled, setEnabled] = useState(alias.cache_enabled)
  const [ttl, setTTL] = useState(String(alias.cache_ttl_seconds || 300))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      await api.updateAliasCache(alias.alias, {
        cache_enabled: enabled,
        cache_ttl_seconds: enabled ? Number(ttl) : 0,
      })
      onSaved()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'failed to update cache')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Cache · ${alias.alias}`}
      description="Identical requests return a cached response. Bypass with X-Gatemux-No-Cache: 1."
      onClose={onClose}
      size="sm"
    >
      <form onSubmit={submit} className="space-y-4">
        <label className="flex cursor-pointer items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-4 w-4"
          />
          Enable cache
        </label>
        <Field label="TTL (seconds)" hint="Common: 300 (5min), 3600 (1h), 86400 (24h)">
          <Input type="number" min="1" disabled={!enabled} value={ttl} onChange={(e) => setTTL(e.target.value)} />
        </Field>
        {err && <p className="text-xs text-danger-fg">{err}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" loading={busy}>Save</Button>
        </div>
      </form>
    </Modal>
  )
}

type CapKey =
  | 'chat'
  | 'stream_chat'
  | 'embeddings'
  | 'moderation'
  | 'rerank'
  | 'images'
  | 'audio_transcribe'
  | 'audio_speech'
  | 'messages_passthrough'

const CAPABILITY_COLUMNS: { key: CapKey; label: string; hint: string }[] = [
  { key: 'chat', label: 'Chat', hint: '/v1/chat/completions' },
  { key: 'stream_chat', label: 'Stream', hint: 'streaming chat' },
  { key: 'embeddings', label: 'Embed', hint: '/v1/embeddings' },
  { key: 'moderation', label: 'Moderate', hint: '/v1/moderations' },
  { key: 'rerank', label: 'Rerank', hint: '/v1/rerank' },
  { key: 'images', label: 'Images', hint: '/v1/images' },
  { key: 'audio_transcribe', label: 'STT', hint: '/v1/audio/transcriptions' },
  { key: 'audio_speech', label: 'TTS', hint: '/v1/audio/speech' },
  { key: 'messages_passthrough', label: 'Messages', hint: '/v1/messages (Anthropic)' },
]

function CapabilityMatrix({ deployments }: { deployments: Deployment[] | null }) {
  if (deployments === null) {
    return (
      <div className="data-card" style={{ padding: 16 }}>
        <span className="muted">Loading…</span>
      </div>
    )
  }
  if (deployments.length === 0) {
    return (
      <div className="data-card" style={{ padding: 16 }}>
        <span className="muted">No deployments yet — add one to see what surfaces it supports.</span>
      </div>
    )
  }
  return (
    <div className="data-card" style={{ padding: 0, overflowX: 'auto' }}>
      <div className="data-head">
        <h2 className="data-h">
          Capability matrix <span className="data-count tnum">{deployments.length}</span>
        </h2>
        <span className="muted">Per-deployment surface support — dash means provider default</span>
      </div>
      <table className="data-table" style={{ width: '100%', minWidth: 720 }}>
        <thead>
          <tr>
            <th style={{ textAlign: 'left' }}>Deployment</th>
            <th style={{ textAlign: 'left' }}>Provider</th>
            {CAPABILITY_COLUMNS.map((c) => (
              <th key={c.key} title={c.hint} style={{ textAlign: 'center', fontWeight: 500 }}>
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {deployments.map((d) => (
            <tr key={d.name} className="data-row">
              <td className="mono">{d.name}</td>
              <td className="muted mono">{d.provider_type}</td>
              {CAPABILITY_COLUMNS.map((c) => (
                <td key={c.key} style={{ textAlign: 'center' }}>
                  <CapabilityCell capabilities={d.capabilities} cap={c.key} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function CapabilityCell({
  capabilities,
  cap,
}: {
  capabilities: Deployment['capabilities']
  cap: CapKey
}) {
  if (!capabilities) {
    return <span className="muted" title="provider default">—</span>
  }
  const v = capabilities[cap]
  if (v === true) {
    return <span style={{ color: 'var(--success)' }} title="enabled">●</span>
  }
  if (v === false) {
    return <span className="muted" title="disabled">○</span>
  }
  return <span className="muted" title="not set">—</span>
}

function CapabilityBadges({ deployment }: { deployment: Deployment }) {
  const c = deployment.capabilities
  if (!c) return <span className="muted">Provider default</span>
  const flags: string[] = []
  if (c.chat) flags.push('chat')
  if (c.stream_chat) flags.push('stream')
  if (c.embeddings) flags.push('embed')
  if (flags.length === 0) return <span className="muted">None</span>
  return <span>{flags.join(', ')}</span>
}

// ConfigBadge marks entries the gateway config file defines. The file is
// re-applied at every start, so console edits to them don't last.
function ConfigBadge() {
  return (
    <span className="source-badge" title="Defined in the gateway config file. It is re-applied at every start, so edit the file to change this for good.">
      Config file
    </span>
  )
}
