import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { GuardrailCatalogRow, GuardrailPolicy, GuardrailTestResult } from '../types'
import { Button, Field, Input, Select, Textarea, Table, Tr, Td, EmptyRow, useConfirm } from './ui'

const blank = (): GuardrailPolicy => ({ name: '', type: 'banned_terms', mode: 'block', phase: 'both', terms: [] })

export default function GuardrailsPanel() {
  const [rows, setRows] = useState<GuardrailCatalogRow[]>([])
  const [scope, setScope] = useState('team')
  const [subject, setSubject] = useState('')
  const [loaded, setLoaded] = useState<{ scope: string; subject: string; expected: GuardrailPolicy[] } | null>(null)
  const [policies, setPolicies] = useState<GuardrailPolicy[]>([])
  const [draft, setDraft] = useState(blank)
  const [terms, setTerms] = useState('')
  const [editing, setEditing] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [catalogError, setCatalogError] = useState('')
  const [notice, setNotice] = useState('')
  // Known teams and aliases feed the scope picker so nobody has to recall a
  // slug from memory; when the lookup fails the picker degrades to manual entry.
  const [subjects, setSubjects] = useState<{ team: string[]; alias: string[] }>({ team: [], alias: [] })
  const version = useRef(0)
  const confirm = useConfirm()

  async function refresh() {
    try { setRows(await api.listGuardrails()); setCatalogError('') }
    catch { setCatalogError('Unable to load the guardrail catalog. Use Refresh to retry.') }
  }
  useEffect(() => { void refresh(); return () => { version.current++ } }, [])
  useEffect(() => {
    void (async () => {
      try {
        const [teams, aliases] = await Promise.all([
          api.listTeams({ limit: 500 }),
          api.listAliases({ limit: 500 }),
        ])
        setSubjects({
          team: teams.items.map(t => t.slug).sort(),
          alias: aliases.items.map(a => a.alias).sort(),
        })
      } catch { /* picker falls back to manual entry */ }
    })()
  }, [])

  async function load(s = scope, id = subject.trim()) {
    const current = ++version.current
    setBusy(true); setError(''); setNotice(''); setLoaded(null)
    try {
      const expected = await api.getGuardrailScope(s, id)
      if (current !== version.current) return
      setScope(s); setSubject(id); setLoaded({ scope: s, subject: id, expected })
      // Unsupported legacy configuration can be deliberately replaced, never
      // presented as a successfully enforced rule by the editor.
      if (!Array.isArray(expected) || expected.some(p => p.type !== 'banned_terms' || !Array.isArray(p.terms))) {
        setPolicies([]); setError('Legacy or invalid configuration: traffic fails closed. Saving a replacement will remove the invalid rules.')
      } else { setPolicies(expected) }
      setDraft(blank()); setTerms(''); setEditing(null)
    } catch (e) { if (current === version.current) setError(e instanceof Error ? e.message : 'Could not load policies') }
    finally { if (current === version.current) setBusy(false) }
  }

  function changeScope(s: string, id: string) {
    version.current++; setScope(s); setSubject(id); setLoaded(null); setBusy(false); setError(''); setNotice('')
  }
  function stage() {
    const values = terms.split('\n').map(t => t.trim()).filter(Boolean)
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/.test(draft.name) || values.length < 1 || values.length > 32 || values.some(t => new TextEncoder().encode(t).length > 128)) {
      setError('Use a 1–64 character rule name and 1–32 terms, at most 128 bytes each.'); return
    }
    if (policies.some((p, i) => p.name === draft.name && i !== editing)) { setError('Rule names must be unique within this scope.'); return }
    const next = { ...draft, terms: values }
    setPolicies(editing === null ? [...policies, next] : policies.map((p, i) => i === editing ? next : p))
    setDraft(blank()); setTerms(''); setEditing(null); setError(''); setNotice('Unsaved changes. Save policies to apply them.')
  }
  async function save() {
    if (!loaded || busy) return
    const target = loaded
    const beforeConfirm = version.current
    if (!await confirm({ title: 'Apply guardrail policies?', description: `Replace the policies assigned to ${target.scope} ${target.subject}? ${policies.length === 0 ? 'This removes protection from this scope.' : 'Protected traffic is restricted to supported plain-text chat.'}`, confirmLabel: 'Save policies', destructive: policies.length === 0 })) return
    if (beforeConfirm !== version.current) return
    const current = ++version.current
    setBusy(true); setError('')
    try {
      const saved = await api.setGuardrailScope(target.scope, target.subject, target.expected, policies)
      if (current !== version.current) return
      setLoaded({ ...target, expected: saved }); setPolicies(saved); setNotice('Policies saved and audited. New requests use the updated assignment.'); void refresh()
    } catch (e) { if (current === version.current) setError(e instanceof Error ? e.message : 'Could not save policies') }
    finally { if (current === version.current) setBusy(false) }
  }

  return <>
    <div className="scope-notice" style={{ marginBottom: 20 }}><div><strong>Literal-term policies (beta)</strong><p>Case-insensitive literal terms, not PII or prompt-injection detection. Team and model rules both apply. Protected requests bypass cache. Tools, multimodal content, native APIs and unsupported extensions are rejected. Streams use bounded cross-chunk filtering.</p></div></div>
    <section className="card" style={{ marginBottom: 28 }}>
      <div>
        <h2 className="card-title">Assign policies</h2>
        <p className="muted" style={{ margin: '4px 0 0', fontSize: 13.5 }}>Pick a team or model alias to view and edit the rules that apply to it.</p>
      </div>
      <form onSubmit={e => { e.preventDefault(); void load() }} style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'end' }}>
        <Field id="guard-scope" label="Scope"><Select id="guard-scope" value={scope} onChange={e => changeScope(e.target.value, '')}><option value="team">Team</option><option value="alias">Model alias</option></Select></Field>
        {(scope === 'team' ? subjects.team : subjects.alias).length > 0 ? (
          <Field id="guard-subject" label={scope === 'team' ? 'Team' : 'Model alias'}>
            <Select id="guard-subject" value={subject} onChange={e => {
              const value = e.target.value
              if (value) void load(scope, value)
              else changeScope(scope, '')
            }}>
              <option value="">Choose {scope === 'team' ? 'a team' : 'an alias'}…</option>
              {(scope === 'team' ? subjects.team : subjects.alias).map(s => <option key={s} value={s}>{s}</option>)}
            </Select>
          </Field>
        ) : (
          <>
            <Field id="guard-subject" label={scope === 'team' ? 'Team slug' : 'Model alias'}><Input id="guard-subject" value={subject} maxLength={256} onChange={e => changeScope(scope, e.target.value)} required /></Field>
            <Button type="submit" disabled={busy || !subject.trim()}>Load policies</Button>
          </>
        )}
      </form>
      {error && <p role="alert" className="text-[13.5px] text-danger-fg" style={{ margin: 0 }}>{error}</p>}
      {notice && <p role="status" className="muted" style={{ margin: 0, fontSize: 13.5 }}>{notice}</p>}
      {loaded && <>
        <h3 className="card-title" style={{ marginTop: 8 }}>{loaded.scope === 'team' ? 'Team' : 'Alias'} <span className="mono">{loaded.subject}</span></h3>
        <p className="muted" style={{ margin: 0, fontSize: 13.5 }}>Up to eight rules per scope. Changes stay in draft until you save.</p>
        {policies.map((p, i) => <div key={i} style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'center', margin: '12px 0' }}>
          <span><strong>{p.name}</strong> · {p.mode} · {p.phase} · {p.terms.length} terms</span>
          <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => { setDraft(p); setTerms(p.terms.join('\n')); setEditing(i) }}>Edit {p.name}</Button>
          <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => { setPolicies(policies.filter((_, j) => i !== j)); setEditing(null); setDraft(blank()); setTerms(''); setNotice('Unsaved removal. Save policies to apply it.') }}>Remove {p.name}</Button>
        </div>)}
        <form onSubmit={e => { e.preventDefault(); stage() }}>
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <Field id="guard-name" label="Rule name"><Input id="guard-name" value={draft.name} maxLength={64} onChange={e => setDraft({ ...draft, name: e.target.value })} required /></Field>
            <Field id="guard-mode" label="Action"><Select id="guard-mode" value={draft.mode} onChange={e => setDraft({ ...draft, mode: e.target.value as GuardrailPolicy['mode'] })}><option value="block">Block</option><option value="redact">Redact</option><option value="flag">Flag (audit only)</option></Select></Field>
            <Field id="guard-phase" label="Inspection phase"><Select id="guard-phase" value={draft.phase} onChange={e => setDraft({ ...draft, phase: e.target.value as GuardrailPolicy['phase'] })}><option value="both">Request and response</option><option value="pre">Request only</option><option value="post">Response only</option></Select></Field>
          </div>
          <div style={{ margin: '16px 0' }}><Field id="guard-terms" label="Literal terms (one per line)"><Textarea id="guard-terms" value={terms} maxLength={8192} onChange={e => setTerms(e.target.value)} required /></Field></div>
          <Button type="submit" variant="ghost" disabled={busy || (editing === null && policies.length >= 8)}>{editing === null ? 'Add rule to draft' : 'Update draft rule'}</Button>
        </form>
        <div style={{ marginTop: 16 }}><Button type="button" disabled={busy} onClick={() => void save()}>Save policies</Button></div>
      </>}
    </section>
    {loaded && <GuardrailTester policies={policies} />}
    <h2 className="card-title" style={{ marginBottom: 12 }}>Assigned rules</h2>
    {catalogError && <p role="alert">{catalogError}</p>}
    {rows.length === 500 && <p>Showing the first 500 rules. Load a scope directly to manage additional rules.</p>}
    <Table head={['Rule', 'Scope', 'Action', 'Hits / blocks (24h)', '']}>
      {rows.length === 0 ? <EmptyRow cols={5}>No guardrails are assigned yet. Pick a team or model alias above to add its first rule.</EmptyRow> : rows.map((row, i) => <Tr key={i}>
        <Td>{row.name}</Td><Td><span className="muted">{row.scope_type === 'team' ? 'Team' : 'Alias'}</span> <span className="mono">{row.scope_id}</span></Td><Td>{row.mode.charAt(0).toUpperCase() + row.mode.slice(1)}</Td><Td className="tnum">{row.hits_24h.toLocaleString()} / {row.blocks_24h.toLocaleString()}</Td>
        <Td align="right"><button type="button" className="linkish" disabled={busy} onClick={() => void load(row.scope_type, row.scope_id)}>Edit</button></Td>
      </Tr>)}
    </Table>
  </>
}

const DECISION_LABEL: Record<string, string> = {
  allow: 'Allowed', block: 'Blocked', redact: 'Redacted', flag: 'Flagged', skipped: 'Not inspected',
}

// GuardrailTester runs sample text through the draft rules, saved or not,
// so a rule can be checked before it reaches real traffic.
function GuardrailTester({ policies }: { policies: GuardrailPolicy[] }) {
  const [text, setText] = useState('')
  const [phase, setPhase] = useState<'pre' | 'post'>('pre')
  const [result, setResult] = useState<GuardrailTestResult | null>(null)
  const [error, setError] = useState('')
  const [running, setRunning] = useState(false)

  async function run() {
    setRunning(true); setError('')
    try { setResult(await api.testGuardrails({ text, phase, policies })) }
    catch (e) { setResult(null); setError(e instanceof Error ? e.message : 'Could not run the test') }
    finally { setRunning(false) }
  }

  return (
    <section className="card" style={{ marginBottom: 28 }} aria-label="Test rules">
      <div>
        <h2 className="card-title">Test rules</h2>
        <p className="muted" style={{ margin: '4px 0 0', fontSize: 13.5 }}>Runs sample text through the rules above, including unsaved changes. Nothing is logged or sent upstream.</p>
      </div>
      <form onSubmit={e => { e.preventDefault(); void run() }} className="space-y-4">
        <Field id="guard-test-text" label="Sample text"><Textarea id="guard-test-text" value={text} maxLength={16384} rows={4} onChange={e => setText(e.target.value)} placeholder="Paste a prompt or a model response" required /></Field>
        <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'end' }}>
          <Field id="guard-test-phase" label="Treat as"><Select id="guard-test-phase" value={phase} onChange={e => setPhase(e.target.value as 'pre' | 'post')}><option value="pre">A request</option><option value="post">A response</option></Select></Field>
          <Button type="submit" variant="ghost" loading={running} disabled={!text.trim() || policies.length === 0}>Run test</Button>
        </div>
        {policies.length === 0 && <p className="muted small" style={{ margin: 0 }}>Add a rule to test it.</p>}
      </form>
      {error && <p role="alert" className="text-[13.5px] text-danger-fg" style={{ margin: 0 }}>{error}</p>}
      {result && (
        <div className="guard-result" role="status">
          <div className={'guard-verdict is-' + result.decision}>{DECISION_LABEL[result.decision] ?? result.decision}</div>
          <ul className="guard-rules">
            {result.results.map(r => (
              <li key={r.name}><span>{r.name}</span><span className="muted">{DECISION_LABEL[r.decision] ?? r.decision}</span></li>
            ))}
          </ul>
          {result.decision === 'redact' && <pre className="guard-output">{result.output}</pre>}
        </div>
      )}
    </section>
  )
}
