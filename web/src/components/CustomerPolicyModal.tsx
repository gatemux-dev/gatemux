import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { Customer } from '../types'
import { Button, Field, Input, Modal, Select } from './ui'

export default function CustomerPolicyModal({ slug, customer, onClose, onSaved }: { slug: string; customer: Customer; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(customer.name)
  const [budget, setBudget] = useState(customer.usd_limit_cents == null ? '' : String(customer.usd_limit_cents))
  const [period, setPeriod] = useState(customer.period || 'month')
  const [rpm, setRPM] = useState(customer.rpm == null ? '' : String(customer.rpm))
  const [tpm, setTPM] = useState(customer.tpm == null ? '' : String(customer.tpm))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [summary, setSummary] = useState<{ used: number; spend: number; period: string } | null>(null)
  useEffect(() => {
    let active = true
    Promise.all([api.getCustomerBudget(slug, customer.external_id), api.getSpendReport({ team: slug, customer: customer.external_id })])
      .then(([b, s]) => { if (active) setSummary({ used: b.used_cents, spend: s.total.cost_cents, period: b.period }) })
      .catch(() => { if (active) setError('Could not load spend and budget usage. You can still edit the policy.') })
    return () => { active = false }
  }, [slug, customer.external_id])
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try {
      await api.updateCustomer(slug, customer.external_id, { name, usd_limit_cents: budget === '' ? null : Number(budget), period, rpm: rpm === '' ? null : Number(rpm), tpm: tpm === '' ? null : Number(tpm) })
      onSaved()
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Could not save customer policy') }
    finally { setBusy(false) }
  }
  return <Modal title="Customer budget and rates" onClose={onClose}>
    <form onSubmit={submit} className="form-grid">
      <p className="muted">{customer.external_id} · Policies apply across this team’s keys and replicas.</p>
      {summary && <p className="muted" role="status">Budget used or reserved this {summary.period}: {summary.used}¢. Recorded spend in the last 30 days: {summary.spend}¢.</p>}
      <Field id="customer-policy-name" label="Customer name"><Input id="customer-policy-name" value={name} onChange={e => setName(e.target.value)} /></Field>
      <Field id="customer-budget" label="Budget (cents)" hint="Blank means unlimited. Zero allows no positive-cost generation. Usage is measured in UTC day/month windows."><Input id="customer-budget" type="number" min="0" max="1000000000000" step="1" value={budget} onChange={e => setBudget(e.target.value)} /></Field>
      <Field id="customer-period" label="Budget period"><Select id="customer-period" value={period} onChange={e => setPeriod(e.target.value)}><option value="month">Calendar month (UTC)</option><option value="day">Calendar day (UTC)</option></Select></Field>
      <Field id="customer-rpm" label="Customer RPM" hint="Blank or zero means unlimited."><Input id="customer-rpm" type="number" min="0" max="1000000000" step="1" value={rpm} onChange={e => setRPM(e.target.value)} /></Field>
      <Field id="customer-tpm" label="Customer TPM" hint="Estimated input plus reserved output per fixed minute; cache hits also count. Blank or zero means unlimited."><Input id="customer-tpm" type="number" min="0" max="1000000000" step="1" value={tpm} onChange={e => setTPM(e.target.value)} /></Field>
      <p className="muted small">A cost or TPM policy requires accounted endpoints (chat, embeddings, Responses). Unpriced native/opaque endpoints will reject these requests. Missing chat output limits default to 1024 for these customers.</p>
      {error && <p role="alert" className="muted" style={{ color: 'var(--danger-fg)' }}>{error}</p>}
      <div className="modal-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" loading={busy}>Save customer policy</Button></div>
    </form>
  </Modal>
}
