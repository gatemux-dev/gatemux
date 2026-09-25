import { useState } from 'react'
import { api } from '../api/client'
import type { CreateKeyResponse, TeamMember } from '../types'
import { Button, Disclosure, Field, Input, Modal, ModalFooter, MultiSelect, SearchSelect, Textarea } from './ui'
import KeyBudgetField from './KeyBudgetField'
import { parseKeyBudgetUSD } from '../lib/keyBudget'

export default function IssueKeyModal({
  slug,
  availableAliases,
  onClose,
  onIssued,
}: {
  slug: string
  availableAliases: string[]
  onClose: () => void
  onIssued: (resp: CreateKeyResponse) => void
}) {
  const [owner, setOwner] = useState<TeamMember | null>(null)
  const [name, setName] = useState('')
  const [rpm, setRPM] = useState('')
  const [tpm, setTPM] = useState('')
  const [maxParallel, setMaxParallel] = useState('')
  const [usdLimit, setUsdLimit] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [metadataText, setMetadataText] = useState('')
  const [selectedAliases, setSelectedAliases] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const limitsSummary = [
    usdLimit && `$${usdLimit} budget`,
    rpm && `${rpm} RPM`,
    tpm && `${tpm} TPM`,
    maxParallel && `${maxParallel} concurrent`,
    expiresAt && 'expires',
  ].filter(Boolean).join(', ') || 'Team limits apply'

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      let metadata: Record<string, unknown> | undefined
      if (metadataText.trim() !== '') {
        const parsed = JSON.parse(metadataText) as Record<string, unknown>
        if (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object') {
          throw new Error('Metadata must be a JSON object, for example {"service": "billing"}.')
        }
        metadata = parsed
      }
      const r = await api.createKey(slug, {
        name: name.trim() || undefined,
        user_id: owner?.id,
        metadata,
        allowed_models: selectedAliases.length > 0 ? selectedAliases : undefined,
        rpm: rpm ? Number(rpm) : undefined,
        tpm: tpm ? Number(tpm) : undefined,
        max_parallel_requests: maxParallel ? Number(maxParallel) : undefined,
        usd_limit_cents: parseKeyBudgetUSD(usdLimit),
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      })
      onIssued(r)
    } catch (e) {
      setErr(e instanceof SyntaxError ? 'Metadata is not valid JSON.' : e instanceof Error ? e.message : 'Could not issue the key.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Issue a virtual key"
      description={<>For team <span className="mono">{slug}</span>. The full key is shown once, right after you create it.</>}
      onClose={onClose}
      dismissible={false}
    >
      <form onSubmit={submit} className="space-y-5">
        <Field label="Name" hint="Helps you recognise the key in logs, for example the service that uses it.">
          <Input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="billing-service" autoFocus />
        </Field>
        <Field label="Owner" hint="A shared key belongs to the team. Assign it to a member when their personal budget should apply.">
          <SearchSelect<TeamMember>
            value={owner}
            onChange={setOwner}
            emptyChoice="Shared team key"
            placeholder="Search team members…"
            search={(q) => api.listTeamMembers(slug, { limit: 20, offset: 0 }, q).then((p) => p.items.filter((u) => !u.disabled_at))}
            label={(u) => u.name || u.email}
            detail={(u) => u.email}
          />
        </Field>
        <Field
          label="Allowed models"
          hint={selectedAliases.length === 0 ? 'None selected, so the key can use every model the team can.' : `${selectedAliases.length} selected. The key can only call these.`}
        >
          {availableAliases.length === 0
            ? <p className="text-[13px] text-fg-subtle">The team has no models listed yet; the key inherits the team policy.</p>
            : <MultiSelect options={availableAliases} value={selectedAliases} onChange={setSelectedAliases} placeholder="Search models…" />}
        </Field>

        <Disclosure title="Limits" summary={limitsSummary}>
          <KeyBudgetField value={usdLimit} onChange={setUsdLimit} />
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Requests per minute">
              <Input type="number" min="0" value={rpm} onChange={(e) => setRPM(e.target.value)} placeholder="No key limit" />
            </Field>
            <Field label="Tokens per minute">
              <Input type="number" min="0" value={tpm} onChange={(e) => setTPM(e.target.value)} placeholder="No key limit" />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Concurrent requests" hint="Shared across gateway replicas; stacks with the team cap.">
              <Input type="number" min="1" value={maxParallel} onChange={(e) => setMaxParallel(e.target.value)} placeholder="No key limit" />
            </Field>
            <Field label="Expires">
              <Input type="datetime-local" value={expiresAt} onChange={(e) => setExpiresAt(e.target.value)} />
            </Field>
          </div>
        </Disclosure>

        <Disclosure title="Metadata" summary={metadataText.trim() ? 'Set' : 'Optional'}>
          <Field label="Metadata JSON" hint="Attached to the key and shown in logs and exports.">
            <Textarea value={metadataText} onChange={(e) => setMetadataText(e.target.value)} rows={4} placeholder='{"service": "billing"}' />
          </Field>
        </Disclosure>

        {err && <p role="alert" className="text-[13px] text-danger-fg">{err}</p>}
        <ModalFooter>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" loading={busy}>{busy ? 'Creating…' : 'Create key'}</Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}
