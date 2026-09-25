import { useState } from 'react'
import type { CreateKeyResponse } from '../types'
import { Button, Modal, ModalFooter } from './ui'

export default function RevealKeyModal({
  resp,
  onClose,
}: {
  resp: CreateKeyResponse
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(resp.key)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard may be blocked; user can select manually */
    }
  }
  return (
    <Modal
      title="Save your key"
      description="This is the only time the full key is displayed. Copy it now and store it somewhere safe — anyone with this key can consume the budget attached to its owner."
      onClose={onClose}
    >
      <div className="break-all rounded-md border border-warning bg-warning-subtle p-3 font-mono text-xs text-warning-fg">
        {resp.key}
      </div>
      <ModalFooter>
        <Button variant="secondary" onClick={copy}>
          {copied ? 'Copied' : 'Copy'}
        </Button>
        <Button onClick={onClose}>Done</Button>
      </ModalFooter>
    </Modal>
  )
}
