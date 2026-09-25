import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { Customer, Team } from '../types'
import { fmtUSD } from '../lib/money'
import { Pagination } from './Pagination'
import ConcurrencyPolicyModal from './ConcurrencyPolicyModal'
import CustomerPolicyModal from './CustomerPolicyModal'
import { Button, EmptyRow, Field, Input, Select, Table, Td, Tr, useToast } from './ui'

export default function CustomersPanel({ slug }: { slug: string }) {
  const [rows, setRows] = useState<Customer[] | null>(null)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState({ limit: 50, offset: 0 })
  const [externalID, setExternalID] = useState('')
  const [name, setName] = useState('')
  const [limit, setLimit] = useState('')
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<Customer | null>(null)
  const [policyEditing, setPolicyEditing] = useState<Customer | null>(null)
  const [mode, setMode] = useState<NonNullable<Team['customer_registration']>>('optional')
  const [loadedMode, setLoadedMode] = useState(false)
  const [policyBusy, setPolicyBusy] = useState(false)
  const [error, setError] = useState('')
  const toast = useToast()
  const reload = async () => {
    try {
      setError('')
      const [data, team] = await Promise.all([api.listCustomers(slug, page), api.getTeam(slug)])
      setRows(data.items)
      setTotal(data.total)
      setMode(team.customer_registration || 'optional'); setLoadedMode(true)
    } catch (e) { setError('Could not load customers or registration policy.'); toast.error('Could not load customers', e instanceof ApiError ? e.message : undefined) }
  }
  useEffect(() => { void reload() }, [slug, page.limit, page.offset])
  const create = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    try {
      await api.createCustomer(slug, { external_id: externalID, name, max_parallel_requests: limit === '' ? null : Number(limit) })
      setExternalID(''); setName(''); setLimit('')
      toast.success('Customer created')
      await reload()
    } catch (e) { toast.error('Could not create customer', e instanceof ApiError ? e.message : undefined) }
    finally { setBusy(false) }
  }
  return <div className="space-y-4">
    <p className="muted">Your trusted application identifies customers with X-Gatemux-Customer-Id, X-Customer-Id, or the request’s user field, in that order. These identifiers are attribution, not end-user authentication. Limits apply within this team across all keys and replicas. Generic passthrough uses headers only.</p>
    {error && <p role="alert">{error} <button className="linkish" onClick={() => void reload()}>Retry</button></p>}
    <form className="data-card form-grid" style={{ padding: 16 }} onSubmit={async e => {
      e.preventDefault(); setPolicyBusy(true)
      try { await api.setCustomerRegistration(slug, mode); toast.success('Customer registration policy updated') }
      catch (e) { toast.error('Could not save registration policy', e instanceof ApiError ? e.message : undefined) }
      finally { setPolicyBusy(false) }
    }}>
      <Field id="customer-registration" label="Customer registration policy" hint="Required and automatic modes reject requests without identity. Automatic registration is capped at 10,000 customers per team; existing customers retain their configured limits.">
        <Select id="customer-registration" value={mode} disabled={!loadedMode} onChange={e => setMode(e.target.value as typeof mode)}><option value="optional">Optional — unknown IDs are attributed without registration</option><option value="required">Required — only pre-registered customers</option><option value="auto_create">Automatic — register supplied identities</option></Select>
      </Field>
      <div className="modal-actions"><Button type="submit" disabled={!loadedMode} loading={policyBusy}>Save registration policy</Button></div>
    </form>
    <Table head={['Customer ID', 'Name', 'Budget / period', 'RPM / TPM', 'Maximum concurrent', '']} footer={<Pagination total={total} limit={page.limit} offset={page.offset} onChange={setPage} />}>
      {rows === null ? <EmptyRow cols={6}>{error ? 'Customers unavailable.' : 'Loading…'}</EmptyRow> : rows.length === 0 ? <EmptyRow cols={6}>No customers registered.</EmptyRow> : rows.map(row =>
        <Tr key={row.id}><Td mono>{row.external_id}</Td><Td>{row.name || '—'}</Td><Td>{row.usd_limit_cents == null ? <span className="muted">Unlimited</span> : `${fmtUSD(row.usd_limit_cents)} / ${row.period}`}</Td><Td num>{row.rpm || row.tpm ? `${row.rpm?.toLocaleString() ?? '—'} / ${row.tpm?.toLocaleString() ?? '—'}` : <span className="muted">Unlimited</span>}</Td><Td num>{row.max_parallel_requests ?? <span className="muted">Unlimited</span>}</Td>
          <Td align="right"><button className="linkish" onClick={() => setPolicyEditing(row)}>Budget & rates</button>{' · '}<button className="linkish" onClick={() => setEditing(row)}>Concurrency</button></Td>
        </Tr>)}
    </Table>
    <form onSubmit={create} className="data-card form-grid" style={{ padding: 16 }}>
      <h3 className="data-h">Register a customer</h3>
      <Field id="new-customer-id" label="Customer ID"><Input id="new-customer-id" required maxLength={256} value={externalID} onChange={e => setExternalID(e.target.value)} /></Field>
      <Field id="new-customer-name" label="Name"><Input id="new-customer-name" value={name} onChange={e => setName(e.target.value)} /></Field>
      <Field id="new-customer-concurrency" label="Maximum concurrent requests" hint="Leave blank for unlimited. Policy is loaded for each identified request."><Input id="new-customer-concurrency" type="number" min="1" step="1" value={limit} onChange={e => setLimit(e.target.value)} /></Field>
      <div className="modal-actions"><Button type="submit" loading={busy}>Create customer</Button></div>
    </form>
    {editing && <ConcurrencyPolicyModal title="Customer concurrency" subject={editing.external_id} current={editing.max_parallel_requests} onClose={() => setEditing(null)}
      onSave={async value => {
        await api.setCustomerConcurrency(slug, editing.external_id, value)
        setEditing(null)
        toast.success('Customer concurrency updated')
        await reload()
      }} />}
    {policyEditing && <CustomerPolicyModal slug={slug} customer={policyEditing} onClose={() => setPolicyEditing(null)} onSaved={() => { setPolicyEditing(null); toast.success('Customer policy updated'); void reload() }} />}
  </div>
}
