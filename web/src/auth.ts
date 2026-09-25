import type { AuthenticatedUser } from './types'

// Account sessions ride on the HttpOnly gatemux_session cookie set by the
// server; the SPA never reads or stores the token. Master-key login is
// the one place we still hold a credential in JS — it's break-glass and
// expected to be transient, so it lives in sessionStorage (cleared when
// the tab closes) instead of localStorage.
const MASTER_KEY = 'gatemux.masterKey'

// Migration shims for users who already have the old localStorage entries.
// Wiped on first load so leftover session tokens don't sit in localStorage
// forever after the cookie cutover.
const LEGACY_TOKEN_KEY = 'aiport.token'
const LEGACY_USER_KEY = 'aiport.user'
const LEGACY_ADMIN_KEY = 'aiport.adminKey'

;(function migrateLegacy() {
  const previousMaster = window.sessionStorage.getItem('aiport.masterKey')
  if (previousMaster && !window.sessionStorage.getItem(MASTER_KEY)) {
    window.sessionStorage.setItem(MASTER_KEY, previousMaster)
  }
  window.sessionStorage.removeItem('aiport.masterKey')
  // If the user had a master key in localStorage from before, hoist it
  // into sessionStorage so they don't have to re-enter it once.
  // window.-qualified: Node 22+ ships an experimental bare `localStorage`
  // global that shadows the DOM one under test runners.
  const legacyAdmin = window.localStorage.getItem(LEGACY_ADMIN_KEY) || window.localStorage.getItem(LEGACY_TOKEN_KEY)
  if (legacyAdmin && !window.sessionStorage.getItem(MASTER_KEY)) {
    // Heuristic: the token looks like a master key only if there was no
    // session user object alongside. Account-session tokens become useless
    // after the cookie cutover anyway, so dropping them is safe.
    if (!window.localStorage.getItem(LEGACY_USER_KEY)) {
      window.sessionStorage.setItem(MASTER_KEY, legacyAdmin)
    }
  }
  window.localStorage.removeItem(LEGACY_TOKEN_KEY)
  window.localStorage.removeItem(LEGACY_USER_KEY)
  window.localStorage.removeItem(LEGACY_ADMIN_KEY)
})()

export const LOGOUT_EVENT = 'gatemux:logout'

export type Principal = {
  token: string
  user: AuthenticatedUser | null
  isMasterKey: boolean
}

export function getMasterKey(): string | null {
  return window.sessionStorage.getItem(MASTER_KEY)
}

// setMasterKey is the only persistence path that still lives in JS. The
// account-login flow now relies on the HttpOnly cookie and never calls
// this.
export function setMasterKey(key: string) {
  window.sessionStorage.setItem(MASTER_KEY, key)
}

// reason travels with the logout event so the login screen can explain
// an involuntary sign-out ('expired') instead of looking like a random
// logout; deliberate logouts pass nothing.
export function clearSession(reason?: 'expired') {
  window.sessionStorage.removeItem(MASTER_KEY)
  window.dispatchEvent(new CustomEvent(LOGOUT_EVENT, { detail: { reason } }))
}

// loadPrincipalSync returns a master-key principal when one is stashed in
// sessionStorage. Account sessions need an async /auth/me call (the
// cookie isn't readable from JS), so they bootstrap from null and fill
// in from whoami once the API responds.
export function loadPrincipalSync(): Principal | null {
  const mk = getMasterKey()
  if (!mk) return null
  return { token: mk, user: null, isMasterKey: true }
}

export type Role = 'admin' | 'manager' | 'member'

// principalRole derives a single source of truth for nav/route gating.
// Master-key callers map to 'admin' (synthetic super-admin). Sessions
// issued before the role field shipped fall back to is_admin so their
// experience doesn't break on first reload.
export function principalRole(p: Principal | null): Role {
  if (!p) return 'member'
  if (p.isMasterKey) return 'admin'
  if (!p.user) return 'member'
  const r = p.user.role
  if (r === 'admin' || r === 'manager' || r === 'member') return r
  return p.user.is_admin ? 'admin' : 'member'
}

export function principalIsAdmin(p: Principal | null): boolean {
  return principalRole(p) === 'admin'
}

export function principalIsManager(p: Principal | null): boolean {
  return principalRole(p) === 'manager'
}

export function principalTeamSlug(p: Principal | null): string | undefined {
  return p?.user?.team_slug || undefined
}
