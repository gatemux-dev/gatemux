import { useState } from 'react'
import type { CreateInviteResponse } from '../types'
import { Button, Modal, ModalFooter } from './ui'

export default function RevealInviteModal({
  resp,
  onClose,
}: {
  resp: CreateInviteResponse
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const fullURL = window.location.origin + resp.url

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(fullURL)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* user can select manually */
    }
  }

  return (
    <Modal
      title="Invite link"
      description="Share this link with the invitee. The token is shown only once and is single-use."
      onClose={onClose}
    >
      <dl className="grid gap-3 sm:grid-cols-2">
        <Field label="Role">
          <span className={resp.role === 'admin' ? 'text-warning-fg' : 'text-fg-base'}>
            {resp.role}
          </span>
        </Field>
        <Field label="Team">
          {resp.team_slug || <span className="text-fg-subtle">individual</span>}
        </Field>
        <Field label="Email">
          {resp.email || <span className="text-fg-subtle">any</span>}
        </Field>
        <Field label="Expires">
          {new Date(resp.expires_at).toLocaleString()}
        </Field>
      </dl>

      <div className="mt-4 break-all rounded-md border border-warning bg-warning-subtle p-3 font-mono text-xs text-warning-fg">
        {fullURL}
      </div>

      <ModalFooter>
        <Button variant="secondary" onClick={copy}>
          {copied ? 'Copied' : 'Copy link'}
        </Button>
        <Button onClick={onClose}>Done</Button>
      </ModalFooter>
    </Modal>
  )
}

function Field({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="rounded-md border border-border-base bg-bg-surface p-2">
      <dt className="text-xs text-fg-subtle">{label}</dt>
      <dd className="mt-0.5 text-sm">{children}</dd>
    </div>
  )
}
