import { useState } from 'react'
import { ApiError } from '../api/client'
import { Button, Field, Input, Modal } from './ui'

export default function ConcurrencyPolicyModal({
  title,
  subject,
  current,
  onClose,
  onSave,
}: {
  title: string
  subject: string
  current?: number
  onClose: () => void
  onSave: (value: number | null) => Promise<void>
}) {
  const [value, setValue] = useState(current != null ? String(current) : '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      await onSave(value === '' ? null : Number(value))
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : 'Could not update concurrency')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form onSubmit={submit} className="form-grid" style={{ marginTop: 8 }}>
        <p className="muted small">{subject}</p>
        <Field label="Maximum concurrent requests" hint="Shared across every GateMux replica through Redis. Leave blank for unlimited.">
          <Input type="number" min="1" autoFocus value={value} onChange={(e) => setValue(e.target.value)} placeholder="unlimited" />
        </Field>
        {err && <div className="muted small" style={{ color: 'var(--danger-fg)' }}>{err}</div>}
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose} type="button">Cancel</Button>
          <Button type="submit" loading={busy}>Save</Button>
        </div>
      </form>
    </Modal>
  )
}
