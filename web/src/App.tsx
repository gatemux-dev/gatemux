import { Suspense, lazy, useEffect, useState } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import {
  loadPrincipalSync,
  LOGOUT_EVENT,
  principalRole,
  type Principal,
} from './auth'
import { api, ApiError } from './api/client'
import Login from './pages/Login'
import Layout from './components/Layout'

// Every page is a route-level chunk: the console used to ship all 18
// pages in one bundle, so member-role users downloaded the whole admin
// surface for their four pages. Login and Layout stay static — they're
// needed on first paint.
const Reset = lazy(() => import('./pages/Reset'))
const Signup = lazy(() => import('./pages/Signup'))
const TeamsList = lazy(() => import('./pages/TeamsList'))
const TeamDetail = lazy(() => import('./pages/TeamDetail'))
const UsageList = lazy(() => import('./pages/UsageList'))
const AliasDetail = lazy(() => import('./pages/AliasDetail'))
const Users = lazy(() => import('./pages/Users'))
const Invites = lazy(() => import('./pages/Invites'))
const Settings = lazy(() => import('./pages/Settings'))
const Models = lazy(() => import('./pages/Models'))
const Account = lazy(() => import('./pages/Account'))
const MyKeys = lazy(() => import('./pages/MyKeys'))
const MyUsage = lazy(() => import('./pages/MyUsage'))
const Spend = lazy(() => import('./pages/Spend'))
const Playground = lazy(() => import('./pages/Playground'))
const Setup = lazy(() => import('./pages/Setup'))
const Overview = lazy(() => import('./pages/Overview'))

// Route chunks resolve in well under a second; a quiet placeholder beats
// a layout flash.
function RouteFallback() {
  return <div className="p-8 text-sm text-fg-subtle">Loading…</div>
}

export default function App() {
  // Master-key principals come back synchronously (sessionStorage); for
  // account sessions the cookie isn't readable from JS, so we have to
  // ask /auth/me. `loading` is true on the very first paint until that
  // resolves, then we either show the app or the login screen.
  const [principal, setPrincipal] = useState<Principal | null>(loadPrincipalSync())
  const [loading, setLoading] = useState<boolean>(principal === null)
  // Set when a 401 signed the user out mid-session, so the login screen
  // can say why they landed there. The URL is untouched, so signing back
  // in resumes on the same page.
  const [sessionExpired, setSessionExpired] = useState(false)

  useEffect(() => {
    const onLogout = (e: Event) => {
      setPrincipal(null)
      setSessionExpired((e as CustomEvent).detail?.reason === 'expired')
    }
    window.addEventListener(LOGOUT_EVENT, onLogout)
    return () => window.removeEventListener(LOGOUT_EVENT, onLogout)
  }, [])

  useEffect(() => {
    // If we already have a principal (master key), no need to whoami.
    if (principal) {
      setLoading(false)
      return
    }
    let alive = true
    api
      .whoami()
      .then((resp) => {
        if (!alive) return
        if (resp.user) {
          setPrincipal({ token: '', user: resp.user, isMasterKey: false })
        } else if (resp.is_master_key) {
          setPrincipal({ token: '', user: null, isMasterKey: true })
        }
      })
      .catch((e) => {
        // 401 is the expected "not signed in" response; don't surface it.
        if (!(e instanceof ApiError) || e.status !== 401) {
          // Other errors are also not actionable on the login screen;
          // swallow and let the user try logging in.
        }
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-bg-base text-fg-subtle">
        <span className="text-sm">Loading…</span>
      </div>
    )
  }

  return (
    <BrowserRouter>
      <Suspense fallback={<RouteFallback />}>
        <Routes>
          <Route
            path="/signup/:token"
            element={<Signup onLogin={setPrincipal} />}
          />
          <Route path="/reset/:token" element={<Reset />} />
          <Route
            path="/*"
            element={
              principal ? (
                <Authenticated principal={principal} />
              ) : (
                <Login
                  onLogin={(p) => {
                    setSessionExpired(false)
                    setPrincipal(p)
                  }}
                  notice={sessionExpired ? 'Your session expired. Sign in again to pick up where you left off.' : undefined}
                />
              )
            }
          />
        </Routes>
      </Suspense>
    </BrowserRouter>
  )
}

function Authenticated({ principal }: { principal: Principal }) {
  const role = principalRole(principal)
  let routes: React.ReactNode
  switch (role) {
    case 'admin':
      routes = <AdminRoutes principal={principal} />
      break
    case 'manager':
      routes = <ManagerRoutes principal={principal} />
      break
    default:
      routes = <UserRoutes principal={principal} />
  }
  return (
    <Layout principal={principal}>
      <Suspense fallback={<RouteFallback />}>{routes}</Suspense>
    </Layout>
  )
}

function AdminRoutes({ principal }: { principal: Principal }) {
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/overview" replace />} />
      <Route path="/overview" element={<Overview />} />
      <Route path="/teams" element={<TeamsList />} />
      <Route path="/teams/:slug" element={<TeamDetail principal={principal} />} />
      <Route path="/users" element={<Users principal={principal} />} />
      <Route path="/invites" element={<InvitesRedirect />} />
      <Route path="/models" element={<Models />} />
      <Route path="/models/:alias" element={<AliasDetail />} />
      <Route path="/usage" element={<UsageList principal={principal} />} />
      <Route path="/spend" element={<Spend principal={principal} />} />
      <Route path="/playground" element={<Playground principal={principal} />} />
      <Route path="/settings" element={<Settings />} />
      <Route path="/providers" element={<Settings section="providers" />} />
      <Route path="/guardrails" element={<Settings section="guardrails" />} />
      <Route path="/alerts" element={<Settings section="alerts" />} />
      <Route path="/passthroughs" element={<Settings section="passthroughs" />} />
      <Route path="/audit" element={<Settings section="audit" />} />
      <Route path="/concurrency" element={<Settings section="concurrency" />} />
      <Route path="/setup" element={<Setup />} />
      <Route path="*" element={<Navigate to="/overview" replace />} />
    </Routes>
  )
}

// Admins manage invitations as a tab of Users; old links keep working.
function InvitesRedirect() {
  const { search } = useLocation()
  const params = new URLSearchParams(search)
  params.set('tab', 'invites')
  return <Navigate to={`/users?${params}`} replace />
}

function UserRoutes({ principal }: { principal: Principal }) {
  const user = principal.user!
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/account" replace />} />
      <Route path="/account" element={<Account user={user} />} />
      <Route path="/keys" element={<MyKeys />} />
      <Route path="/usage" element={<MyUsage />} />
      <Route path="/playground" element={<Playground principal={principal} />} />
      <Route path="*" element={<Navigate to="/account" replace />} />
    </Routes>
  )
}

// ManagerRoutes mirrors the admin team-management surface but constrained
// to the manager's own team. Shared pages use scoped catalogs and hide
// administrator-only actions; backend authorization remains authoritative.
function ManagerRoutes({ principal }: { principal: Principal }) {
  const user = principal.user!
  const slug = user.team_slug
  const home = slug ? `/teams/${slug}` : '/account'
  return (
    <Routes>
      <Route path="/" element={<Navigate to={home} replace />} />
      <Route path="/teams/:slug" element={<TeamDetail principal={principal} />} />
      <Route path="/invites" element={<Invites principal={principal} />} />
      <Route path="/usage" element={<UsageList principal={principal} />} />
      <Route path="/spend" element={<Spend principal={principal} />} />
      <Route path="/playground" element={<Playground principal={principal} />} />
      <Route path="/account" element={<Account user={user} />} />
      <Route path="*" element={<Navigate to={home} replace />} />
    </Routes>
  )
}
