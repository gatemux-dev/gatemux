import { useEffect, useState } from 'react'
import { AlertTriangle, ChevronDown } from 'lucide-react'
import { setMasterKey, type Principal } from '../auth'
import { api, ApiError } from '../api/client'
import { Button, Field, Input, useToast } from '../components/ui'
import { useTheme } from '../theme'
import { Moon, Sun } from 'lucide-react'
import BrandMark from '../components/BrandMark'

export default function Login({ onLogin, notice }: { onLogin: (p: Principal) => void; notice?: string }) {
  const { theme, toggle } = useTheme()
  // Master-key login is opt-in via server config. Default behavior on the
  // login page is account-only; the emergency-key disclosure only renders
  // when the server reports it as enabled.
  const [masterKeyEnabled, setMasterKeyEnabled] = useState(false)
  const [oidcEnabled, setOidcEnabled] = useState(false)
  const [oidcProvider, setOidcProvider] = useState('')
  const [showMasterKey, setShowMasterKey] = useState(false)

  useEffect(() => {
    api
      .getLoginModes()
      .then((m) => {
        setMasterKeyEnabled(!!m.master_key_enabled)
        setOidcEnabled(!!m.oidc_enabled)
        setOidcProvider(m.oidc_provider_name || 'SSO')
      })
      .catch(() => {
        setMasterKeyEnabled(false)
        setOidcEnabled(false)
      })
  }, [])

  return (
    <div className="login-page">
      <button onClick={toggle} aria-label="Toggle theme" className="login-theme">
        {theme === 'dark' ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
      </button>
      <div className="login-column">
        <div className="login-brand">
          <BrandMark size={26} />
          <span>GateMux</span>
        </div>
        <h1 className="login-title">Sign in</h1>
        <p className="login-sub">Manage models, keys, and spend for your gateway.</p>

        {notice && (
          <div role="status" className="login-notice">
            <AlertTriangle aria-hidden className="h-4 w-4 shrink-0" />
            <span>{notice}</span>
          </div>
        )}

        {oidcEnabled && (
          <>
            <a href="/auth/oidc/start" className="btn btn-ghost btn-size-lg" style={{ width: '100%', justifyContent: 'center' }}>
              Continue with {oidcProvider}
            </a>
            <div className="login-divider"><span>or</span></div>
          </>
        )}
        <AccountForm onLogin={onLogin} />

        {masterKeyEnabled && !showMasterKey && (
          <button type="button" onClick={() => setShowMasterKey(true)} className="login-breakglass">
            <ChevronDown className="h-3.5 w-3.5" />
            Emergency / break-glass admin key
          </button>
        )}

        {masterKeyEnabled && showMasterKey && (
          <div className="login-master">
            <p className="login-master-warn">
              Master-key login is for recovery only. It bypasses normal authentication and has full platform access.
            </p>
            <MasterKeyForm onLogin={onLogin} />
            <button type="button" onClick={() => setShowMasterKey(false)} className="login-breakglass">
              Back to sign in
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

function AccountForm({ onLogin }: { onLogin: (p: Principal) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const toast = useToast()

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      // The server set the HttpOnly session cookie; the response body's
      // token is for CLI use, intentionally ignored here.
      const data = await api.login(email.trim(), password)
      toast.success(`Welcome, ${data.user.name || data.user.email}`)
      onLogin({ token: '', user: data.user, isMasterKey: false })
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <Field label="Email" id="login-email" required>
        <Input
          id="login-email"
          type="email"
          autoFocus
          required
          autoComplete="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
      </Field>
      <Field label="Password" id="login-password" required>
        <Input
          id="login-password"
          type="password"
          required
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="••••••••"
        />
      </Field>
      {err && <p className="text-[13px] text-danger-fg">{err}</p>}
      <Button type="submit" size="lg" fullWidth disabled={!email || !password} loading={busy}>
        Sign in
      </Button>
      <p className="text-[13px] text-fg-subtle">New users join through an invite link from an administrator.</p>
    </form>
  )
}

function MasterKeyForm({ onLogin }: { onLogin: (p: Principal) => void }) {
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    const masterKey = key.trim()
    try {
      const me = await api.verifyMasterKey(masterKey)
      if (!me.is_master_key) {
        throw new ApiError(401, 'authentication_error', 'Invalid admin master key')
      }
      setMasterKey(masterKey)
      onLogin({ token: masterKey, user: null, isMasterKey: true })
    } catch (e) {
      setErr(e instanceof ApiError && e.status === 401
        ? 'Invalid admin master key. Check the server’s GATEMUX_ADMIN_KEY value.'
        : e instanceof ApiError ? e.message : 'Could not verify the admin master key. Please try again.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <Field
        label="Admin master key"
        id="login-master"
        required
        hint={
          <span>
            Set by <code className="rounded bg-bg-subtle px-1 py-0.5 font-mono text-[11px]">GATEMUX_ADMIN_KEY</code> on
            the server. Use it for bootstrap or recovery.
          </span>
        }
      >
        <Input
          id="login-master"
          type="password"
          autoFocus
          autoComplete="off"
          required
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="dev-admin-key"
        />
      </Field>
      {err && <p role="alert" className="text-[13px] text-danger-fg">{err}</p>}
      <Button type="submit" size="lg" fullWidth disabled={!key.trim()} loading={busy}>
        Sign in as admin
      </Button>
    </form>
  )
}
