import { useState } from 'react'
import { api, ApiError } from '../api/client'
import type { ApiKey } from '../types'
import { Button, Field, Input, Modal, ModalFooter, Textarea } from './ui'
import KeyBudgetField from './KeyBudgetField'
import { keyBudgetUSD, parseKeyBudgetUSD } from '../lib/keyBudget'

export default function EditKeyModal({
  apiKey,
  availableAliases,
  onClose,
  onSaved,
}: {
  apiKey: ApiKey
  availableAliases: string[]
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(apiKey.name)
  const [rpm, setRPM] = useState(apiKey.rpm != null ? String(apiKey.rpm) : '')
  const [tpm, setTPM] = useState(apiKey.tpm != null ? String(apiKey.tpm) : '')
  const [maxParallel, setMaxParallel] = useState(
    apiKey.max_parallel_requests != null ? String(apiKey.max_parallel_requests) : '',
  )
  const [usdLimit, setUsdLimit] = useState(
    keyBudgetUSD(apiKey.usd_limit_cents),
  )
  const [expiresAt, setExpiresAt] = useState(
    apiKey.expires_at ? toLocalDatetimeInput(apiKey.expires_at) : '',
  )
  const initialModels =
    apiKey.allowed_models && !apiKey.allowed_models.includes('*')
      ? apiKey.allowed_models
      : []
  const [selectedAliases, setSelectedAliases] = useState<string[]>(initialModels)
  const [allowAll, setAllowAll] = useState(initialModels.length === 0)
  const [metadataText, setMetadataText] = useState(
    JSON.stringify(apiKey.metadata ?? {}, null, 2),
  )
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const toggleAlias = (alias: string) => {
    setSelectedAliases((current) =>
      current.includes(alias)
        ? current.filter((item) => item !== alias)
        : [...current, alias],
    )
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      let metadata: Record<string, unknown> = {}
      if (metadataText.trim() !== '') {
        const parsed = JSON.parse(metadataText) as Record<string, unknown>
        if (parsed === null || Array.isArray(parsed)) {
          throw new Error('metadata must be a JSON object')
        }
        metadata = parsed
      }
      await api.updateKey(apiKey.id, {
        name: name.trim(),
        metadata,
        allowed_models: allowAll ? [] : selectedAliases,
        rpm: rpm === '' ? null : Number(rpm),
        tpm: tpm === '' ? null : Number(tpm),
        max_parallel_requests: maxParallel === '' ? null : Number(maxParallel),
        usd_limit_cents: parseKeyBudgetUSD(usdLimit),
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
      })
      onSaved()
    } catch (e) {
      if (e instanceof ApiError || e instanceof Error) {
        setErr(e.message)
      } else {
        setErr('failed to update key')
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Edit key · ${apiKey.prefix}…`}
      description="Editing affects future requests immediately. The secret value is unchanged — use Rotate if you need a fresh secret."
      onClose={onClose}
      dismissible={false}
    >
      <form onSubmit={submit} className="space-y-4">
        <Field label="Label">
          <Input
            type="text" value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ci-pipeline"
          />
        </Field>

        <Field
          label="Concurrent requests"
          hint="Distributed across gateway replicas and stacked with the team cap."
        >
          <Input
            type="number" min="1" value={maxParallel}
            onChange={(e) => setMaxParallel(e.target.value)}
            placeholder="empty = no per-key cap"
          />
        </Field>

        <Field label="Expires at" hint="Leave empty for no expiry.">
          <Input
            type="datetime-local" value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Key RPM">
            <Input
              type="number" min="0" value={rpm}
              onChange={(e) => setRPM(e.target.value)}
              placeholder="empty = team default"
            />
          </Field>
          <Field label="Key TPM">
            <Input
              type="number" min="0" value={tpm}
              onChange={(e) => setTPM(e.target.value)}
              placeholder="empty = team default"
            />
          </Field>
        </div>

        <KeyBudgetField value={usdLimit} onChange={setUsdLimit} />

        <div>
          <label className="block text-xs font-medium text-fg-base">Allowed models</label>
          <label className="mt-2 flex cursor-pointer items-center gap-2 text-xs text-fg-base">
            <input
              type="checkbox" checked={allowAll}
              onChange={(e) => setAllowAll(e.target.checked)}
              className="h-3.5 w-3.5 accent-accent"
            />
            Inherit team policy (allow every model the team can use)
          </label>
          {!allowAll && (
            availableAliases.length === 0 ? (
              <p className="mt-2 text-xs text-fg-subtle">
                No aliases are configured yet.
              </p>
            ) : (
              <div className="mt-2 max-h-40 overflow-y-auto rounded-md border border-border-base bg-bg-base">
                {availableAliases.map((alias) => (
                  <label
                    key={alias}
                    className="flex cursor-pointer items-center gap-3 border-b border-border-subtle px-3 py-2 text-sm last:border-b-0 hover:bg-bg-raised"
                  >
                    <input
                      type="checkbox"
                      checked={selectedAliases.includes(alias)}
                      onChange={() => toggleAlias(alias)}
                      className="h-3.5 w-3.5 accent-accent"
                    />
                    <span className="font-mono text-xs text-fg-base">{alias}</span>
                  </label>
                ))}
              </div>
            )
          )}
        </div>

        <Field label="Metadata JSON">
          <Textarea
            value={metadataText}
            onChange={(e) => setMetadataText(e.target.value)}
            rows={5}
          />
        </Field>

        {err && <p className="text-xs text-danger">{err}</p>}
        <ModalFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy}>
            {busy ? 'Saving…' : 'Save changes'}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}

// toLocalDatetimeInput converts an ISO timestamp to the YYYY-MM-DDTHH:mm
// format that <input type="datetime-local"> expects, in the user's local
// timezone (since that's what the input renders).
function toLocalDatetimeInput(iso: string): string {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}`
  )
}
