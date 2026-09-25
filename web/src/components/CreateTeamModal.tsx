import { useState } from 'react'
import { api, ApiError } from '../api/client'
import { Button, Field, Input, Modal, ModalFooter } from './ui'

export default function CreateTeamModal({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: () => void
}) {
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [rpm, setRpm] = useState('')
  const [maxParallel, setMaxParallel] = useState('')
  const [usd, setUsd] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      await api.createTeam({
        slug: slug.trim(),
        name: name.trim() || undefined,
        rpm: rpm ? parseInt(rpm, 10) : undefined,
        max_parallel_requests: maxParallel ? parseInt(maxParallel, 10) : undefined,
        usd_limit: usd ? parseInt(usd, 10) : undefined,
      })
      onCreated()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'failed to create team')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Create team" onClose={onClose} dismissible={false}>
      <form onSubmit={submit} className="space-y-4">
        <Field
          label="Slug"
          required
          hint={<>Lowercase identifier embedded in keys (gw-{'{'}slug{'}'}-…).</>}
        >
          <Input
            type="text"
            required
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="research"
          />
        </Field>
        <Field label="Display name">
          <Input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Defaults to slug"
          />
        </Field>
        <Field label="Rate limit (RPM)">
          <Input
            type="number"
            min="1"
            value={rpm}
            onChange={(e) => setRpm(e.target.value)}
            placeholder="e.g. 60 (leave empty for unlimited)"
          />
        </Field>
        <Field
          label="Concurrent requests"
          hint="Shared across all gateway replicas through Redis."
        >
          <Input
            type="number"
            min="1"
            value={maxParallel}
            onChange={(e) => setMaxParallel(e.target.value)}
            placeholder="e.g. 20 (leave empty for unlimited)"
          />
        </Field>
        <Field label="USD budget">
          <Input
            type="number"
            min="1"
            value={usd}
            onChange={(e) => setUsd(e.target.value)}
            placeholder="e.g. 100 (leave empty for unlimited)"
          />
        </Field>
        {err && <p className="text-xs text-danger">{err}</p>}
        <ModalFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={!slug.trim()}>
            {busy ? 'Creating…' : 'Create'}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}
