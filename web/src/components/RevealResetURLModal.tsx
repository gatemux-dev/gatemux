import { useState } from 'react'
import { Modal, useToast } from './ui'

export default function RevealResetURLModal({
  data,
  onClose,
}: {
  data: { email: string; url: string; expires_at: string }
  onClose: () => void
}) {
  const toast = useToast()
  const [copied, setCopied] = useState(false)
  const expires = new Date(data.expires_at)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(data.url)
      setCopied(true)
      toast.success('URL copied')
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // clipboard may be blocked; the URL is selectable in the input
    }
  }

  return (
    <Modal
      title="Share this reset URL"
      description={
        <>
          Send this link to <code className="mono">{data.email}</code> through whatever
          channel you trust (Slack DM, ticket, etc.). The URL stops working in{' '}
          {hoursUntil(expires)} or once the user opens it and sets a new password.
        </>
      }
      onClose={onClose}
    >
      <div className="mt-2">
        <input
          readOnly
          value={data.url}
          onFocus={(e) => e.currentTarget.select()}
          className="text-input mono"
          style={{ width: '100%', fontSize: 11 }}
        />
        <div className="muted small" style={{ marginTop: 8 }}>
          GateMux does not send email — distribute this link out-of-band. The link is
          single-use and expires {expires.toLocaleString()}.
        </div>
      </div>
      <div className="modal-actions" style={{ marginTop: 16 }}>
        <button className="btn btn-ghost" onClick={onClose}>Done</button>
        <button className="btn btn-primary" onClick={copy}>
          {copied ? 'Copied' : 'Copy URL'}
        </button>
      </div>
    </Modal>
  )
}

function hoursUntil(t: Date): string {
  const ms = t.getTime() - Date.now()
  const h = Math.max(0, Math.round(ms / 3_600_000))
  if (h <= 1) return 'about an hour'
  if (h < 36) return `${h}h`
  return `${Math.round(h / 24)}d`
}
