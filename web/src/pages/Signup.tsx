import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import { type Principal } from '../auth'
import type { GetInviteResponse } from '../types'
import { Badge, Button, Field, Input, useToast } from '../components/ui'

export default function Signup({ onLogin }: { onLogin: (p: Principal) => void }) {
  const { token = '' } = useParams<{ token: string }>()
  const navigate = useNavigate()
  const [invite, setInvite] = useState<GetInviteResponse | null>(null)
  const [loadErr, setLoadErr] = useState<string | null>(null)

  useEffect(() => {
    api
      .getInvite(token)
      .then(setInvite)
      .catch((e) => setLoadErr(e instanceof ApiError ? e.message : 'invalid invite'))
  }, [token])

  if (loadErr) return <Shell title="Invite not valid" body={loadErr} />
  if (!invite) return <Shell title="Loading" body="Validating invite…" />

  return (
    <Form
      invite={invite}
      token={token}
      onLogin={(p) => {
        onLogin(p)
        navigate('/', { replace: true })
      }}
    />
  )
}

function Form({
  invite,
  token,
  onLogin,
}: {
  invite: GetInviteResponse
  token: string
  onLogin: (p: Principal) => void
}) {
  const [email, setEmail] = useState(invite.email ?? '')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const toast = useToast()

  const emailLocked = !!invite.email
  const passwordsMismatch = confirm.length > 0 && password !== confirm

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (password.length < 8) {
      setErr('Password must be at least 8 characters')
      return
    }
    if (password !== confirm) {
      setErr('Passwords do not match')
      return
    }
    setBusy(true)
    setErr(null)
    try {
      const r = await api.acceptInvite(token, {
        email: email.trim(),
        name: name.trim() || undefined,
        password,
      })
      // Cookie was set by /invite/accept; the response token is for
      // CLI users only and intentionally not persisted in JS.
      toast.success('Account created')
      onLogin({ token: '', user: r.user, isMasterKey: false })
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'Signup failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Shell title="Join GateMux" subtitle="Accept your invite to get started">
      <dl className="mb-4 grid gap-2 text-xs">
        <Detail label="Role">
          <Badge tone={invite.role === 'admin' ? 'warning' : 'neutral'}>{invite.role}</Badge>
        </Detail>
        <Detail label="Team">
          {invite.team_slug ? (
            <span className="font-mono text-fg-base">{invite.team_slug}</span>
          ) : (
            <span className="text-fg-subtle">individual</span>
          )}
        </Detail>
      </dl>

      <form onSubmit={submit} className="space-y-4">
        <Field label="Email" required hint={emailLocked ? `Pinned to ${invite.email}` : undefined}>
          <Input
            type="email"
            required
            disabled={emailLocked}
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
        <Field label="Display name" hint="Optional">
          <Input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Pat Doe"
          />
        </Field>
        <Field label="Password" required hint="At least 8 characters">
          <Input
            type="password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field
          label="Confirm password"
          required
          error={passwordsMismatch ? 'Passwords do not match' : null}
        >
          <Input
            type="password"
            required
            invalid={passwordsMismatch}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </Field>
        {err && <p className="text-xs text-danger">{err}</p>}
        <Button
          type="submit"
          fullWidth
          loading={busy}
          disabled={!email || !password || !confirm || passwordsMismatch}
        >
          Create account
        </Button>
      </form>
    </Shell>
  )
}

function Shell({
  title,
  subtitle,
  body,
  children,
}: {
  title: string
  subtitle?: string
  body?: string
  children?: React.ReactNode
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-bg-base px-4 py-12">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <span className="inline-flex h-12 w-12 items-center justify-center rounded-xl bg-accent text-accent-fg shadow-md">
            <span className="text-lg font-semibold">A</span>
          </span>
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-fg-base">{title}</h1>
            {subtitle && <p className="text-sm text-fg-muted">{subtitle}</p>}
          </div>
        </div>
        <div className="rounded-xl border border-border-base bg-bg-surface p-6 shadow-sm">
          {body && <p className="text-center text-sm text-fg-muted">{body}</p>}
          {children}
        </div>
      </div>
    </div>
  )
}

function Detail({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between rounded-md border border-border-subtle bg-bg-raised px-3 py-2">
      <dt className="text-fg-subtle">{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}
