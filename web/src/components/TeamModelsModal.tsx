import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import { Button, Field, Modal } from './ui'

// Edits the team's model allowlist against the live alias catalog. Entries
// that no longer match a catalog alias are kept visible so saving cannot
// silently drop a restriction someone configured for a not-yet-created alias.
export default function TeamModelsModal({
  subject,
  current,
  onClose,
  onSave,
}: {
  subject: string
  current: string[]
  onClose: () => void
  onSave: (models: string[]) => Promise<void>
}) {
  const allowAll = current.includes('*')
  const [all, setAll] = useState(allowAll)
  const [selected, setSelected] = useState<Set<string>>(new Set(allowAll ? [] : current))
  const [catalog, setCatalog] = useState<string[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    void api.listAliases({ limit: 500 })
      .then(page => setCatalog(page.items.map(a => a.alias).sort()))
      .catch(() => setCatalog([]))
  }, [])

  const stale = [...selected].filter(m => catalog !== null && !catalog.includes(m)).sort()
  const toggle = (alias: string) => {
    const next = new Set(selected)
    if (next.has(alias)) next.delete(alias)
    else next.add(alias)
    setSelected(next)
  }

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!all && selected.size === 0) {
      setErr('Select at least one model, or allow all models.')
      return
    }
    setBusy(true)
    setErr(null)
    try {
      await onSave(all ? ['*'] : [...selected].sort())
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : 'Could not update model access')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Team model access" onClose={onClose}>
      <form onSubmit={submit} className="form-grid" style={{ marginTop: 8 }}>
        <p className="muted small">{subject}</p>
        <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <input type="checkbox" checked={all} onChange={e => setAll(e.target.checked)} />
          <span>Allow every model alias</span>
        </label>
        {!all && (
          <Field label="Allowed models" hint="Keys in this team can only call the checked aliases; key-level restrictions narrow this further.">
            {catalog === null ? (
              <p className="muted small">Loading catalog…</p>
            ) : (
              <div style={{ maxHeight: 260, overflowY: 'auto', display: 'grid', gap: 4 }}>
                {catalog.map(alias => (
                  <label key={alias} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <input type="checkbox" checked={selected.has(alias)} onChange={() => toggle(alias)} />
                    <span className="mono small">{alias}</span>
                  </label>
                ))}
                {stale.map(alias => (
                  <label key={alias} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <input type="checkbox" checked onChange={() => toggle(alias)} />
                    <span className="mono small">{alias}</span>
                    <span className="muted small">not in catalog</span>
                  </label>
                ))}
                {catalog.length === 0 && stale.length === 0 && <p className="muted small">No aliases configured yet.</p>}
              </div>
            )}
          </Field>
        )}
        {err && <div className="muted small" style={{ color: 'var(--danger-fg)' }}>{err}</div>}
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose} type="button">Cancel</Button>
          <Button type="submit" loading={busy}>Save</Button>
        </div>
      </form>
    </Modal>
  )
}
