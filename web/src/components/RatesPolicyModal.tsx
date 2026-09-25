import { useState } from 'react'
import { ApiError } from '../api/client'
import { Button, Field, Input, Modal } from './ui'

export default function RatesPolicyModal({
  title,
  subject,
  currentRPM,
  currentTPM,
  onClose,
  onSave,
}: {
  title: string
  subject: string
  currentRPM?: number
  currentTPM?: number
  onClose: () => void
  onSave: (body: { rpm: number | null; tpm: number | null }) => Promise<void>
}) {
  const [rpm, setRPM] = useState(currentRPM != null ? String(currentRPM) : '')
  const [tpm, setTPM] = useState(currentTPM != null ? String(currentTPM) : '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      await onSave({
        rpm: rpm === '' ? null : Number(rpm),
        tpm: tpm === '' ? null : Number(tpm),
      })
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : 'Could not update rate limits')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form onSubmit={submit} className="form-grid" style={{ marginTop: 8 }}>
        <p className="muted small">{subject}</p>
        <Field label="Requests per minute" hint="Shared by every key in this team. Leave blank for unlimited.">
          <Input type="number" min="1" autoFocus value={rpm} onChange={(e) => setRPM(e.target.value)} placeholder="unlimited" />
        </Field>
        <Field label="Tokens per minute" hint="Prompt plus completion tokens. Leave blank for unlimited.">
          <Input type="number" min="1" value={tpm} onChange={(e) => setTPM(e.target.value)} placeholder="unlimited" />
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
