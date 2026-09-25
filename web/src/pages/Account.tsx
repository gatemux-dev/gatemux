import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Activity, ChevronRight, KeyRound, Laptop, MessageSquare, Trash2 } from 'lucide-react'
import { api, ApiError } from '../api/client'
import { useQuery } from '../lib/useQuery'
import type { AuthenticatedUser, SessionRow } from '../types'
import {
  Badge,
  Button,
  ErrorState,
  Field,
  Input,
  MetricStrip,
  PageHeader,
  StatTile,
  useConfirm,
  useToast,
} from '../components/ui'

export default function Account({ user }: { user: AuthenticatedUser }) {
  const {
    data: budget,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(() => api.getMyBudget(), [])

  const limit = budget?.limit_cents ?? null
  const used = budget?.used_cents ?? 0
  const ratio = limit && limit > 0 ? Math.min(1, used / limit) : 0
  const pct = Math.round(ratio * 100)
  const tone: 'success' | 'warning' | 'danger' =
    ratio >= 0.95 ? 'danger' : ratio >= 0.75 ? 'warning' : 'success'

  return (
    <>
      <PageHeader
        title="Account"
        description="Your profile and current-period budget."
      />

      <MetricStrip>
        <StatTile label="Email" value={<span className="mono text-base">{user.email}</span>} />
        <StatTile label="Display name" value={user.name || '—'} />
        <StatTile
          label="Team"
          value={user.team_slug ? <span className="mono">{user.team_slug}</span> : 'individual'}
        />
        <StatTile
          label="Role"
          value={
            <Badge tone={user.is_admin ? 'accent' : 'neutral'} monospace={false}>
              {user.is_admin ? 'admin' : 'user'}
            </Badge>
          }
        />
      </MetricStrip>

      <section className="data-card" style={{ padding: 18 }}>
        <h2 className="data-h">Budget this period</h2>
        {loading ? (
          <div className="muted" style={{ padding: '12px 0' }}>Loading…</div>
        ) : error && budget === null ? (
          <ErrorState
            title="Couldn't load budget"
            message={error.message}
            onRetry={reload}
            retrying={refreshing}
          />
        ) : budget === null || limit == null || limit <= 0 ? (
          <p className="muted" style={{ marginTop: 8 }}>
            No personal budget configured.{' '}
            {user.team_slug ? (
              <>
                Your usage counts against team{' '}
                <code className="mono" style={{ background: 'var(--bg-subtle)', padding: '1px 6px', borderRadius: 4 }}>
                  {user.team_slug}
                </code>
                's limits.
              </>
            ) : (
              <>Ask an admin to set a personal budget.</>
            )}
          </p>
        ) : (
          <div style={{ marginTop: 12 }}>
            <div className="flex flex-wrap items-baseline justify-between gap-3">
              <div>
                <div className="tnum" style={{ fontSize: 28, fontWeight: 600, letterSpacing: '-0.015em' }}>
                  ${(used / 100).toFixed(2)}
                  <span className="muted" style={{ marginLeft: 6, fontSize: 14, fontWeight: 400 }}>
                    / ${(limit / 100).toFixed(2)}
                  </span>
                </div>
                <div className="muted" style={{ marginTop: 2, color: `var(--${tone === 'danger' ? 'danger' : tone === 'warning' ? 'warning' : 'success'}-fg)` }}>
                  {pct}% of {budget.period} limit used
                </div>
              </div>
              <div className="muted" style={{ textAlign: 'right' }}>
                <div>Period: {budget.period}</div>
                {budget.window_end && <div>Resets {formatRelative(new Date(budget.window_end))}</div>}
              </div>
            </div>
            <div className={`progress progress-${tone === 'danger' ? 'err' : tone === 'warning' ? 'warn' : 'ok'}`} style={{ marginTop: 14 }}>
              <div className="progress-fill" style={{ width: `${Math.max(2, pct)}%` }} />
            </div>
          </div>
        )}
      </section>

      <SecurityPanel />

      <section>
        <h2 className="data-h" style={{ marginBottom: 10 }}>Quick links</h2>
        <div className="grid gap-3 sm:grid-cols-3">
          <QuickLink to="/keys" label="My keys" sub="View keys assigned to you" icon={KeyRound} />
          <QuickLink to="/usage" label="My usage" sub="Per-request log & spend" icon={Activity} />
          <QuickLink to="/playground" label="Playground" sub="Test an alias from the browser" icon={MessageSquare} />
        </div>
      </section>
    </>
  )
}

// SecurityPanel groups password change + active sessions on the Account
// page. Both pieces are user-scoped and need a current session to act,
// so they live together rather than under a global Settings page.
function SecurityPanel() {
  return (
    <section className="data-card" style={{ padding: 18 }}>
      <h2 className="data-h">Security</h2>
      <div className="alias-grid" style={{ marginTop: 12 }}>
        <ChangePasswordCard />
        <ActiveSessionsCard />
      </div>
    </section>
  )
}

function ChangePasswordCard() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirmNext, setConfirmNext] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const toast = useToast()

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null)
    if (next.length < 8) {
      setErr('New password must be at least 8 characters.')
      return
    }
    if (next !== confirmNext) {
      setErr('New passwords do not match.')
      return
    }
    setBusy(true)
    try {
      await api.changePassword(current, next)
      toast.success('Password updated')
      setCurrent('')
      setNext('')
      setConfirmNext('')
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'Could not change password')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="card">
      <h3 className="card-title">Change password</h3>
      <form onSubmit={submit} className="form-grid" style={{ marginTop: 8 }}>
        <Field label="Current password" required>
          <Input
            type="password"
            required
            value={current}
            autoComplete="current-password"
            onChange={(e) => setCurrent(e.target.value)}
          />
        </Field>
        <Field label="New password" required hint="At least 8 characters.">
          <Input
            type="password"
            required
            value={next}
            autoComplete="new-password"
            onChange={(e) => setNext(e.target.value)}
          />
        </Field>
        <Field label="Confirm new password" required>
          <Input
            type="password"
            required
            value={confirmNext}
            autoComplete="new-password"
            onChange={(e) => setConfirmNext(e.target.value)}
          />
        </Field>
        {err && <div className="muted small" style={{ color: 'var(--danger-fg)' }}>{err}</div>}
        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <Button type="submit" loading={busy} disabled={!current || !next || !confirmNext}>
            Update password
          </Button>
        </div>
      </form>
    </section>
  )
}

function ActiveSessionsCard() {
  const toast = useToast()
  const confirm = useConfirm()

  const {
    data: sessions,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(() => api.listMySessions(), [])

  const revokeOne = async (s: SessionRow) => {
    const ok = await confirm({
      title: `Sign out this session?`,
      description: `Created ${formatRelativeISO(s.created_at)} from ${s.ip || 'unknown ip'}.`,
      confirmLabel: 'Sign out',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.revokeSession(s.token_hash_prefix)
      toast.success('Session signed out')
      reload()
    } catch (e) {
      toast.error('Failed to revoke', e instanceof ApiError ? e.message : undefined)
    }
  }

  const revokeOthers = async () => {
    const others = (sessions ?? []).filter((s) => !s.current).length
    if (others === 0) {
      toast.info('No other active sessions')
      return
    }
    const ok = await confirm({
      title: `Sign out ${others} other session${others === 1 ? '' : 's'}?`,
      description: 'Other devices will be signed out immediately. This session stays active.',
      confirmLabel: 'Sign out others',
      destructive: true,
    })
    if (!ok) return
    try {
      const r = await api.revokeOtherSessions()
      toast.success(`Signed out ${r.revoked} session${r.revoked === 1 ? '' : 's'}`)
      reload()
    } catch (e) {
      toast.error('Failed to revoke', e instanceof ApiError ? e.message : undefined)
    }
  }

  return (
    <section className="card">
      <div className="card-head-flex">
        <h3 className="card-title">Active sessions</h3>
        <Button
          variant="ghost"
          onClick={revokeOthers}
          disabled={!sessions || sessions.filter((s) => !s.current).length === 0}
        >
          Sign out other devices
        </Button>
      </div>
      {loading ? (
        <div className="muted small" style={{ padding: 8 }}>Loading…</div>
      ) : error && sessions === null ? (
        <ErrorState
          title="Couldn't load sessions"
          message={error.message}
          onRetry={reload}
          retrying={refreshing}
        />
      ) : (sessions ?? []).length === 0 ? (
        <div className="muted small" style={{ padding: 8 }}>No active sessions.</div>
      ) : (
        <div className="event-list" style={{ marginTop: 4 }}>
          {(sessions ?? []).map((s) => (
            <div key={s.token_hash_prefix} className="event-row" style={{ alignItems: 'center' }}>
              <Laptop size={14} className="event-icon" />
              <div className="event-body">
                <div className="event-rule">
                  {shortenUA(s.user_agent)}
                  {s.current && (
                    <span style={{ marginLeft: 6 }}>
                      <Badge tone="accent" monospace={false}>this device</Badge>
                    </span>
                  )}
                </div>
                <div className="event-detail muted small">
                  {s.ip || 'unknown ip'} · created {formatRelativeISO(s.created_at)}
                  {s.last_seen_at && ` · last seen ${formatRelativeISO(s.last_seen_at)}`}
                </div>
              </div>
              {!s.current && (
                <button
                  className="linkish"
                  style={{ color: 'var(--danger-fg)' }}
                  onClick={() => revokeOne(s)}
                  aria-label="Sign out this session"
                >
                  <Trash2 size={12} />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

function shortenUA(ua: string | undefined): string {
  if (!ua) return 'Unknown device'
  // Heuristic UA → friendly label. Avoids pulling in a UA parser; the
  // session list is informational and operators read this.
  const lower = ua.toLowerCase()
  let browser = 'Browser'
  if (lower.includes('firefox')) browser = 'Firefox'
  else if (lower.includes('edg/')) browser = 'Edge'
  else if (lower.includes('chrome')) browser = 'Chrome'
  else if (lower.includes('safari')) browser = 'Safari'
  else if (lower.includes('curl')) browser = 'curl'
  let os = ''
  if (lower.includes('mac os')) os = 'macOS'
  else if (lower.includes('windows')) os = 'Windows'
  else if (lower.includes('linux')) os = 'Linux'
  else if (lower.includes('android')) os = 'Android'
  else if (lower.includes('iphone') || lower.includes('ipad')) os = 'iOS'
  return os ? `${browser} on ${os}` : browser
}

function formatRelativeISO(when: string): string {
  const ms = Date.now() - new Date(when).getTime()
  if (ms < 0) return new Date(when).toLocaleString()
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 36) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function QuickLink({
  to,
  label,
  sub,
  icon: Icon,
}: {
  to: string
  label: string
  sub: string
  icon: React.ComponentType<any>
}) {
  return (
    <Link
      to={to}
      className="data-card"
      style={{
        padding: 14,
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        textDecoration: 'none',
        color: 'inherit',
      }}
    >
      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          width: 36,
          height: 36,
          borderRadius: 8,
          background: 'var(--accent-subtle)',
          color: 'var(--accent-text)',
        }}
      >
        <Icon size={16} />
      </span>
      <div className="flex-1">
        <div style={{ fontSize: 13, fontWeight: 550 }}>{label}</div>
        <div className="muted" style={{ fontSize: 11 }}>{sub}</div>
      </div>
      <ChevronRight size={14} className="text-fg-subtle" />
    </Link>
  )
}

function formatRelative(date: Date): string {
  const ms = date.getTime() - Date.now()
  const hours = Math.max(0, Math.round(ms / 3_600_000))
  if (hours < 1) return 'within the hour'
  if (hours < 36) return `in ${hours}h`
  return `in ${Math.round(hours / 24)}d`
}
