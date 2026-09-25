import { useState } from 'react'
import { api, ApiError } from '../api/client'
import type { Deployment } from '../types'
import { Button, Field, Input, Modal, ModalFooter, MultiSelect } from './ui'

export default function CreateAliasModal({
  deployments,
  onClose,
  onSaved,
}: {
  deployments: Deployment[]
  onClose: () => void
  onSaved: () => void
}) {
  const [alias, setAlias] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const byName = new Map(deployments.map((d) => [d.name, d]))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      await api.upsertAlias({ alias: alias.trim(), deployments: selected })
      onSaved()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'failed to save alias')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Add a model alias" onClose={onClose} dismissible={false}>
      <form onSubmit={submit} className="space-y-4">
        <Field
          label="Alias"
          required
          hint={
            <>
              The name clients put in the <code>model</code> field of /v1/chat/completions.
            </>
          }
        >
          <Input
            type="text" required value={alias}
            onChange={(e) => setAlias(e.target.value)}
            className="font-mono"
            placeholder="gpt-4"
          />
        </Field>

        <Field
          label="Deployments"
          required
          hint={selected.length > 1
            ? 'Tried in this order: the first is primary, the rest are fallbacks. Use the arrows to reorder.'
            : 'Pick one or more. Add several to get automatic fallback.'}
        >
          {deployments.length === 0 ? (
            <p className="text-[13px] text-fg-subtle">No deployments yet. Add a deployment first.</p>
          ) : (
            <MultiSelect
              ordered
              options={deployments.map((d) => d.name)}
              value={selected}
              onChange={setSelected}
              placeholder="Search deployments…"
              describe={(name) => {
                const d = byName.get(name)
                if (!d) return null
                return d.has_credential ? `${d.provider_type}, ${d.upstream_model}` : `${d.upstream_model}, no credential`
              }}
            />
          )}
        </Field>

        {err && <p role="alert" className="text-[13px] text-danger-fg">{err}</p>}

        <ModalFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            loading={busy}
            disabled={!alias.trim() || selected.length === 0}
          >
            {busy ? 'Saving…' : 'Save alias'}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}
