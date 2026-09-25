import { useEffect, useState } from 'react'
import { Upload } from 'lucide-react'
import { api, ApiError } from '../api/client'
import { useQuery } from '../lib/useQuery'
import { matchPrices, parsePriceList, unpricedDeployments, type PriceMatch } from '../lib/priceImport'
import type { Deployment, Pricing } from '../types'
import { Button, EmptyRow, Field, Input, Modal, ModalFooter, Select, Table, Td, Textarea, Tr, useToast } from './ui'

// PricingPanel is where model prices live: the stored rates, deployments
// that have none (their requests are recorded as unpriced), and an import
// from a GateMux JSON price list.
export default function PricingPanel() {
  const { data, reload } = useQuery(
    () => Promise.all([api.listPricing({ limit: 500 }), api.listDeployments({ limit: 500, offset: 0 })])
      .then(([p, d]) => ({ pricing: p.items, deployments: d.items })),
    [],
  )
  const [importing, setImporting] = useState(false)
  const [pricingPrefill, setPricingPrefill] = useState<Deployment | null>(null)
  const pricing = data?.pricing ?? null
  const deployments = data?.deployments ?? []
  const missing = pricing ? unpricedDeployments(deployments, pricing) : []

  return (
    <div className="page-section">
      {missing.length > 0 && (
        <div className="scope-notice" role="status">
          <div>
            <strong>{missing.length} model{missing.length === 1 ? ' has' : 's have'} no price</strong>
            <p>Their requests are recorded as unpriced and left out of spend totals and budgets. Import a price list or add prices by hand.</p>
            <p className="mono small" style={{ overflowWrap: 'anywhere' }}>
              {missing.slice(0, 8).map((d, i) => (
                <span key={d.name}>{i > 0 && ', '}<button type="button" className="linkish" onClick={() => setPricingPrefill(d)}>{d.provider_type}/{d.upstream_model}</button></span>
              ))}
              {missing.length > 8 && ` and ${missing.length - 8} more`}
            </p>
          </div>
        </div>
      )}
      <div className="flex justify-end">
        <Button variant="ghost" leadingIcon={<Upload size={13} />} onClick={() => setImporting(true)}>Import prices</Button>
      </div>
      <PricingTable pricing={pricing} onChanged={reload} prefill={pricingPrefill} onPrefillUsed={() => setPricingPrefill(null)} />
      {importing && pricing && (
        <ImportPricesModal deployments={deployments} existing={pricing} onClose={() => setImporting(false)} onDone={() => { setImporting(false); reload() }} />
      )}
    </div>
  )
}

function ImportPricesModal({ deployments, existing, onClose, onDone }: {
  deployments: Deployment[]
  existing: Pricing[]
  onClose: () => void
  onDone: () => void
}) {
  const [text, setText] = useState('')
  const [matches, setMatches] = useState<PriceMatch[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const preview = (raw: string) => {
    setText(raw)
    if (!raw.trim()) { setMatches(null); setError(''); return }
    try { setMatches(matchPrices(parsePriceList(raw), deployments, existing)); setError('') }
    catch (e) { setMatches(null); setError(e instanceof Error ? e.message : 'Not valid JSON.') }
  }
  const writes = (matches ?? []).filter((m) => m.status !== 'same')

  const apply = async () => {
    setBusy(true)
    let saved = 0
    try {
      for (const m of writes) {
        const prev = existing.find((p) => p.provider_type === m.provider_type && p.upstream_model === m.upstream_model)
        await api.upsertPricing({
          provider_type: m.provider_type,
          upstream_model: m.upstream_model,
          input_per_million_cents: m.input_per_million_cents,
          output_per_million_cents: m.output_per_million_cents,
          cache_read_per_million_cents: m.cache_read_per_million_cents,
          cache_write_per_million_cents: m.cache_write_per_million_cents,
          cache_write_1h_per_million_cents: prev?.cache_write_1h_per_million_cents ?? null,
          reasoning_per_million_cents: prev?.reasoning_per_million_cents ?? null,
        })
        saved++
      }
      toast.success(`Imported ${saved} price${saved === 1 ? '' : 's'}`)
      onDone()
    } catch (e) {
      toast.error(`Import stopped after ${saved} price${saved === 1 ? '' : 's'}`, e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Import prices" description="Paste or upload a GateMux JSON price list keyed by model name. Each entry needs a provider type and input/output USD costs per token. Only matching deployments are imported." size="lg" onClose={onClose}>
      <div className="space-y-4">
        <Field label="Upload a file">
          <input type="file" accept="application/json,.json" onChange={(e) => { const f = e.target.files?.[0]; if (f) void f.text().then(preview) }} />
        </Field>
        <Field label="Or paste JSON">
          <Textarea rows={6} value={text} onChange={(e) => preview(e.target.value)} placeholder='{ "example-model": { "provider": "openai", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002 } }' className="mono" />
        </Field>
        {error && <p role="alert" className="text-[13.5px] text-danger-fg" style={{ margin: 0 }}>{error}</p>}
        {matches && (
          matches.length === 0 ? (
            <p className="muted" style={{ margin: 0 }}>No deployment matches a model in this list.</p>
          ) : (
            <div style={{ overflowX: "auto" }}>
              <Table head={['Model', <span className="cell-num" key="i">Input ¢/M</span>, <span className="cell-num" key="o">Output ¢/M</span>, 'Change']}>
                {matches.map((m) => (
                  <Tr key={`${m.provider_type}/${m.upstream_model}`}>
                    <Td><span className="mono muted">{m.provider_type}/</span><span className="mono">{m.upstream_model}</span></Td>
                    <Td num mono>{m.input_per_million_cents.toLocaleString()}</Td>
                    <Td num mono>{m.output_per_million_cents.toLocaleString()}</Td>
                    <Td className="muted">{m.status === 'new' ? 'New price' : m.status === 'changed' ? 'Replaces current price' : 'Unchanged'}</Td>
                  </Tr>
                ))}
              </Table>
            </div>
          )
        )}
      </div>
      <ModalFooter>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button onClick={() => void apply()} loading={busy} disabled={writes.length === 0}>
          {writes.length === 0 ? 'Nothing to import' : `Import ${writes.length} price${writes.length === 1 ? '' : 's'}`}
        </Button>
      </ModalFooter>
    </Modal>
  )
}

function PricingTable({ pricing, onChanged, prefill, onPrefillUsed }: { pricing: Pricing[] | null; onChanged: () => void; prefill: Deployment | null; onPrefillUsed: () => void }) {
  const [editing, setEditing] = useState<'new' | Pricing | null>(null)
  const [providerType, setProviderType] = useState('openai')
  const [upstreamModel, setUpstreamModel] = useState('')
  const [inputCents, setInputCents] = useState('')
  const [outputCents, setOutputCents] = useState('')
  const [tiers, setTiers] = useState<Record<string, string>>({})
  const tierFields = [
    ['cache_read_per_million_cents', 'Cache read ¢/M'],
    ['cache_write_per_million_cents', 'Cache write / 5m ¢/M'],
    ['cache_write_1h_per_million_cents', 'Cache write / 1h ¢/M'],
    ['reasoning_per_million_cents', 'Reasoning ¢/M'],
  ] as const
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  useEffect(() => {
    if (!prefill) return
    open('new')
    setProviderType(prefill.provider_type)
    setUpstreamModel(prefill.upstream_model)
    onPrefillUsed()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prefill])

  const open = (row: 'new' | Pricing) => {
    if (row === 'new') {
      setProviderType('openai')
      setUpstreamModel('')
      setInputCents('')
      setOutputCents('')
      setTiers({})
    } else {
      setProviderType(row.provider_type)
      setUpstreamModel(row.upstream_model)
      setInputCents(String(row.input_per_million_cents))
      setOutputCents(String(row.output_per_million_cents))
      setTiers(Object.fromEntries(tierFields.map(([field]) => [field, row[field] == null ? '' : String(row[field])])))
    }
    setEditing(row)
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await api.upsertPricing({
        ...Object.fromEntries(tierFields.map(([field]) => [field, tiers[field]?.trim() ? Number(tiers[field]) : null])),
        provider_type: providerType,
        upstream_model: upstreamModel,
        input_per_million_cents: Number(inputCents),
        output_per_million_cents: Number(outputCents),
      })
      toast.success('Pricing saved')
      setEditing(null)
      onChanged()
    } catch (e) {
      toast.error('Pricing update failed', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Table
        head={[
          'Provider',
          'Model',
          <span className="cell-num" key="i">Input ¢/M</span>,
          <span className="cell-num" key="o">Output ¢/M</span>,
          'Optional tiers ¢/M',
          'Effective at',
          '',
        ]}
        header={
          <div className="data-head">
            <h2 className="data-h">
              Pricing <span className="data-count tnum">{pricing?.length ?? 0}</span>
            </h2>
            <Button size="sm" variant="ghost" onClick={() => open('new')}>Add pricing</Button>
          </div>
        }
      >
        {pricing === null ? (
          <tr><td colSpan={7} className="muted" style={{ padding: 16 }}>Loading prices…</td></tr>
        ) : pricing.length === 0 ? (
          <EmptyRow cols={7}>No prices yet. Import a price list or add one.</EmptyRow>
        ) : (
          pricing.map((p) => (
            <Tr key={`${p.provider_type}:${p.upstream_model}`}>
              <Td><span className="mono muted">{p.provider_type}</span></Td>
              <Td mono>{p.upstream_model}</Td>
              <Td num mono>{p.input_per_million_cents.toLocaleString()}</Td>
              <Td num mono>{p.output_per_million_cents.toLocaleString()}</Td>
              <Td>
                {(() => {
                  // Only overridden tiers earn a line; 'inherit' four times
                  // over was noise.
                  const set = tierFields.filter(([field]) => p[field] != null)
                  if (set.length === 0) return <span className="muted">inherit</span>
                  return (
                    <div className="space-y-0.5 text-xs">
                      {set.map(([field, label]) => (
                        <div key={field}>{label.replace(' ¢/M', '')}: <span className="mono tnum">{p[field]}</span></div>
                      ))}
                    </div>
                  )
                })()}
              </Td>
              <Td className="muted">{new Date(p.effective_at).toLocaleString()}</Td>
              <Td><button type="button" className="linkish" aria-label={`Edit pricing ${p.provider_type}/${p.upstream_model}`} onClick={() => open(p)}>Edit</button></Td>
            </Tr>
          ))
        )}
      </Table>

      {editing && (
      <Modal
        title={editing === 'new' ? 'Add pricing' : `Edit pricing · ${upstreamModel}`}
        description="Rates are cents per million tokens for one provider × model pair."
        size="lg"
        dismissible={false}
        onClose={() => setEditing(null)}
      >
      <form onSubmit={submit}>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Provider type">
            <Select value={providerType} onChange={(e) => setProviderType(e.target.value)}>
              <option>openai</option>
              <option>azure_openai</option>
              <option>anthropic</option>
              <option>mistral</option>
              <option>groq</option>
              <option>together</option>
              <option>fireworks</option>
              <option>openrouter</option>
              <option>cohere</option>
              <option>bedrock</option>
              <option>vertex</option>
              <option>gemini</option>
              <option>openai_compatible</option>
            </Select>
          </Field>
          <Field label="Upstream model" required>
            <Input
              required
              value={upstreamModel}
              onChange={(e) => setUpstreamModel(e.target.value)}
              placeholder="gpt-4-turbo"
            />
          </Field>
          <Field label="Input ¢/M">
            <Input
              type="number"
              min="0"
              required
              value={inputCents}
              onChange={(e) => setInputCents(e.target.value)}
              placeholder="1000"
            />
          </Field>
          <Field label="Output ¢/M">
            <Input
              type="number"
              min="0"
              required
              value={outputCents}
              onChange={(e) => setOutputCents(e.target.value)}
              placeholder="3000"
            />
          </Field>
        </div>
        <fieldset className="mt-4 rounded-lg border border-border-base p-3">
          <legend className="px-1 text-sm font-medium">Cached and reasoning token rates</legend>
          <div className="grid gap-3 sm:grid-cols-2">
            {tierFields.map(([field, label]) => <Field key={field} label={label}>
              <Input aria-label={label} type="number" min="0" step="1" value={tiers[field] ?? ''} onChange={e => setTiers({ ...tiers, [field]: e.target.value })} placeholder="Inherit base rate" />
            </Field>)}
          </div>
          <p className="mt-2 text-xs text-fg-muted">Blank inherits the base input/output rate; 1h writes first inherit the general write rate. Zero makes that tier free. Cached and reasoning tokens are subsets, never an extra charge on top of total tokens. Input and output each round up to whole cents after tier aggregation.</p>
        </fieldset>
        <ModalFooter>
          <Button variant="secondary" onClick={() => setEditing(null)}>Cancel</Button>
          <Button type="submit" loading={busy}>Save pricing</Button>
        </ModalFooter>
      </form>
      </Modal>
      )}
    </>
  )
}
