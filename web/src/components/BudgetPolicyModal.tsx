import { useState } from 'react'
import { Button, Field, Input, Modal, ModalFooter, Select } from './ui'

export default function BudgetPolicyModal({
  title,
  subject,
  currentLimitCents,
  currentPeriod,
  onClose,
  onSave,
}: {
  title: string
  subject: string
  currentLimitCents?: number
  currentPeriod?: string
  onClose: () => void
  onSave: (body: { limit_cents?: number; period: string }) => Promise<void>
}) {
  const [limitCents, setLimitCents] = useState(
    currentLimitCents != null && currentLimitCents > 0 ? String(currentLimitCents) : '',
  )
  const [period, setPeriod] = useState(currentPeriod || 'month')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      const parsed = limitCents.trim() === '' ? undefined : Number.parseInt(limitCents, 10)
      if (parsed != null && (Number.isNaN(parsed) || parsed < 0)) {
        throw new Error('limit must be zero or greater')
      }
      await onSave({ limit_cents: parsed, period })
    } catch (error) {
      if (error instanceof Error) {
        setErr(error.message)
      } else {
        setErr('failed to save budget policy')
      }
      setBusy(false)
      return
    }
    setBusy(false)
  }

  return (
    <Modal title={title} onClose={onClose} dismissible={false}>
      <form onSubmit={submit} className="space-y-4">
        <div className="rounded-xl border border-border-base bg-bg-base p-4">
          <div className="text-[10px] uppercase tracking-[0.18em] text-fg-subtle">Scope</div>
          <div className="mt-2 text-sm text-fg-base">{subject}</div>
          <p className="mt-2 text-xs leading-5 text-fg-subtle">
            Leave the limit empty to remove the spend cap. When present, this policy is
            enforced before traffic is sent upstream.
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-[minmax(0,1.2fr)_160px]">
          <Field label="Limit (cents)">
            <Input
              type="number"
              min="0"
              step="1"
              value={limitCents}
              onChange={(e) => setLimitCents(e.target.value)}
              placeholder="blank = unlimited"
            />
          </Field>
          <Field label="Window">
            <Select value={period} onChange={(e) => setPeriod(e.target.value)}>
              <option value="day">day</option>
              <option value="month">month</option>
            </Select>
          </Field>
        </div>

        {err && (
          <div className="rounded-md border border-danger/30 bg-danger-subtle p-3 text-xs text-danger-fg">
            {err}
          </div>
        )}

        <ModalFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy}>
            {busy ? 'Saving…' : 'Save policy'}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}
