import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { ApiKey } from '../types'
import { keyBudgetUSD, parseKeyBudgetUSD } from '../lib/keyBudget'
import KeyBudgetField from './KeyBudgetField'
import { Button, Modal, ModalFooter } from './ui'

export default function KeyBudgetModal({ apiKey, onClose, onSaved }: { apiKey: ApiKey; onClose: () => void; onSaved: () => void }) {
  const [summary, setSummary] = useState<Awaited<ReturnType<typeof api.getKeyBudget>> | null>(null)
  const [limit, setLimit] = useState('')
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    let active = true
    setError(''); setSummary(null)
    api.getKeyBudget(apiKey.id).then(result => {
      if (active) { setSummary(result); setLimit(keyBudgetUSD(result.limit_cents)) }
    }).catch(() => { if (active) setError('Could not load key budget usage. Retry before editing.') })
    return () => { active = false }
  }, [apiKey.id, attempt])
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!summary || busy || apiKey.revoked_at) return
    setBusy(true); setError('')
    try { await api.setKeyBudget(apiKey.id, parseKeyBudgetUSD(limit)); onSaved() }
    catch (e) { setError(e instanceof Error ? e.message : 'Could not save key budget') }
    finally { setBusy(false) }
  }
  return <Modal title="Key budget" description={`${apiKey.name || apiKey.prefix} · Limits apply to future admissions; existing requests can still settle.`} onClose={onClose} dismissible={!busy}>
    <form onSubmit={submit} className="space-y-4">
      {summary ? <>
        <p role="status">Used or reserved this {summary.period}: ${(summary.used_cents / 100).toFixed(2)}.</p>
        <p className="muted small">UTC window: {summary.window_start} – {summary.window_end}. Includes reserved estimates and earlier usage, even before a cap was enabled.</p>
        {!apiKey.revoked_at && <KeyBudgetField value={limit} onChange={setLimit} />}
        {apiKey.revoked_at && <p>This key is revoked; its budget is read-only.</p>}
        <p className="muted small">This cap belongs to this key ID. Rotation copies the cap to a new key with a separate usage total; team and owner budgets remain shared.</p>
      </> : !error && <p role="status">Loading key budget…</p>}
      {error && <p role="alert">{error}</p>}
      {!summary && error && <Button type="button" onClick={() => setAttempt(n => n + 1)}>Retry budget</Button>}
      <ModalFooter><Button type="button" variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
        {!apiKey.revoked_at && <Button type="submit" loading={busy} disabled={!summary}>Save key budget</Button>}
      </ModalFooter>
    </form>
  </Modal>
}
