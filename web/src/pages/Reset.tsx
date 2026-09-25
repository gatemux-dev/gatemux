import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { Moon, Sun } from 'lucide-react'
import { api, ApiError } from '../api/client'
import { Button, Field, Input, useToast } from '../components/ui'
import { useTheme } from '../theme'

export default function Reset() {
  const { token } = useParams<{ token: string }>()
  const { theme, toggle } = useTheme()
  const [email, setEmail] = useState<string | null>(null)
  const [expiresAt, setExpiresAt] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [tokenError, setTokenError] = useState<string | null>(null)
  const [next, setNext] = useState('')
  const [confirmNext, setConfirmNext] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [done, setDone] = useState(false)
  const [submitErr, setSubmitErr] = useState<string | null>(null)
  const toast = useToast()
  const nav = useNavigate()

  useEffect(() => {
    if (!token) return
    api
      .getPasswordReset(token)
      .then((r) => {
        setEmail(r.email)
        setExpiresAt(r.expires_at)
      })
      .catch((e) => {
        setTokenError(
          e instanceof ApiError && e.status === 404
            ? 'This reset link is invalid or has expired. Ask your admin for a new one.'
            : 'Could not load this reset link. Try again or contact your admin.',
        )
      })
      .finally(() => setLoading(false))
  }, [token])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitErr(null)
    if (next.length < 8) {
      setSubmitErr('Password must be at least 8 characters.')
      return
    }
    if (next !== confirmNext) {
      setSubmitErr('Passwords do not match.')
      return
    }
    if (!token) return
    setSubmitting(true)
    try {
      await api.consumePasswordReset(token, next)
      setDone(true)
      toast.success('Password set — please sign in')
      // Drop the user on the login page after a beat so they can read
      // the success state.
      setTimeout(() => nav('/'), 1500)
    } catch (e) {
      setSubmitErr(e instanceof ApiError ? e.message : 'Reset failed')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-bg-base px-4 py-12">
      <button
        onClick={toggle}
        aria-label="Toggle theme"
        className="absolute right-6 top-6 rounded-md border border-border-base bg-bg-surface p-2 text-fg-muted shadow-sm transition-colors hover:bg-bg-raised hover:text-fg-base"
      >
        {theme === 'dark' ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
      </button>
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <span className="inline-flex h-12 w-12 items-center justify-center rounded-xl bg-accent text-accent-fg shadow-md">
            <span className="text-lg font-semibold">A</span>
          </span>
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-fg-base">Set a new password</h1>
            {email && <p className="text-sm text-fg-muted">for <span className="mono">{email}</span></p>}
          </div>
        </div>

        <div className="rounded-xl border border-border-base bg-bg-surface p-6 shadow-sm">
          {loading ? (
            <p className="text-sm text-fg-subtle">Verifying your link…</p>
          ) : tokenError ? (
            <>
              <p className="text-sm text-danger">{tokenError}</p>
              <div className="mt-4 text-center">
                <Link className="linkish" to="/">Back to sign in</Link>
              </div>
            </>
          ) : done ? (
            <p className="text-sm text-fg-base">
              Your password is set. Redirecting you to sign in…
            </p>
          ) : (
            <form onSubmit={submit} className="space-y-4">
              {expiresAt && (
                <p className="text-xs text-fg-subtle">
                  This link is single-use and expires {new Date(expiresAt).toLocaleString()}.
                </p>
              )}
              <Field label="New password" required hint="At least 8 characters.">
                <Input
                  type="password"
                  required
                  autoFocus
                  autoComplete="new-password"
                  value={next}
                  onChange={(e) => setNext(e.target.value)}
                />
              </Field>
              <Field label="Confirm new password" required>
                <Input
                  type="password"
                  required
                  autoComplete="new-password"
                  value={confirmNext}
                  onChange={(e) => setConfirmNext(e.target.value)}
                />
              </Field>
              {submitErr && <p className="text-xs text-danger">{submitErr}</p>}
              <Button type="submit" fullWidth disabled={!next || !confirmNext} loading={submitting}>
                Set password
              </Button>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
