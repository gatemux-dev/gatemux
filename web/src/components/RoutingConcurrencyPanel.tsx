import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { RoutingConcurrencyLimit } from '../types'
import ConcurrencyPolicyModal from './ConcurrencyPolicyModal'
import { Button, EmptyRow, Field, Input, Select, Table, Td, Tr, useToast } from './ui'

export default function RoutingConcurrencyPanel() {
  const [rows, setRows] = useState<RoutingConcurrencyLimit[] | null>(null)
  const [scope, setScope] = useState<'model' | 'provider'>('model')
  const [subject, setSubject] = useState('')
  const [limit, setLimit] = useState('')
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<RoutingConcurrencyLimit | null>(null)
  // Known aliases feed autocomplete on the subject field; free entry stays
  // possible because a limit may be configured before its alias exists.
  const [aliases, setAliases] = useState<string[]>([])
  const toast = useToast()
  useEffect(() => {
    void api.listAliases({ limit: 500 }).then(page => setAliases(page.items.map(a => a.alias).sort())).catch(() => {})
  }, [])
  const reload = async () => {
    try { setRows(await api.listRoutingConcurrencyLimits()) }
    catch (e) { toast.error('Could not load concurrency limits', e instanceof ApiError ? e.message : undefined) }
  }
  useEffect(() => { void reload() }, [])
  const save = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    try {
      await api.setRoutingConcurrencyLimit({ scope, subject, max_parallel_requests: Number(limit) })
      setSubject('')
      setLimit('')
      toast.success('Concurrency limit saved')
      await reload()
    } catch (e) { toast.error('Could not save concurrency limit', e instanceof ApiError ? e.message : undefined) }
    finally { setBusy(false) }
  }
  return <div className="space-y-4">
    <p className="muted">Limits are shared across all teams and replicas. A model limit covers its alias for the whole request; a provider limit covers every active upstream call using that provider type. Changes reach other replicas within five seconds.</p>
    <Table head={['Scope', 'Subject', 'Maximum concurrent', '']}>
      {rows === null ? <EmptyRow cols={4}>Loading…</EmptyRow> : rows.length === 0 ? <EmptyRow cols={4}>No routing concurrency limits configured.</EmptyRow> : rows.map(row =>
        <Tr key={row.id}>
          <Td>{row.scope === 'model' ? 'Model alias' : 'Provider type'}</Td>
          <Td mono>{row.subject}</Td>
          <Td mono>{row.max_parallel_requests ?? 'unlimited'}</Td>
          <Td align="right"><button className="linkish" onClick={() => setEditing(row)}>Edit</button></Td>
        </Tr>)}
    </Table>
    <form onSubmit={save} className="data-card form-grid" style={{ padding: 16 }}>
      <h3 className="data-h">Set a concurrency limit</h3>
      <Field label="Scope"><Select value={scope} onChange={e => setScope(e.target.value as 'model' | 'provider')}>
        <option value="model">Model alias</option><option value="provider">Provider type</option>
      </Select></Field>
      <Field label={scope === 'model' ? 'Model alias' : 'Provider type'} hint={scope === 'model' ? 'Pick a configured alias, or type a name to set the limit before the alias is created.' : 'Exact deployment provider type, for example openai or openai_compatible.'}>
        <Input required maxLength={256} value={subject} onChange={e => setSubject(e.target.value)} list={scope === 'model' ? 'routing-concurrency-aliases' : undefined} />
        {scope === 'model' && <datalist id="routing-concurrency-aliases">{aliases.map(a => <option key={a} value={a} />)}</datalist>}
      </Field>
      <Field label="Maximum concurrent requests"><Input required type="number" min="1" step="1" value={limit} onChange={e => setLimit(e.target.value)} /></Field>
      <div className="modal-actions"><Button type="submit" loading={busy}>Save limit</Button></div>
    </form>
    {editing && <ConcurrencyPolicyModal title="Edit routing concurrency" subject={`${editing.scope}: ${editing.subject}`} current={editing.max_parallel_requests ?? undefined}
      onClose={() => setEditing(null)} onSave={async value => {
        await api.setRoutingConcurrencyLimit({ scope: editing.scope, subject: editing.subject, max_parallel_requests: value })
        setEditing(null)
        toast.success('Concurrency limit updated')
        await reload()
      }} />}
  </div>
}
