import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import SectionTabs, { useSectionTab } from '../components/SectionTabs'
import Invites from './Invites'
import type { Principal } from '../auth'
import { Plus, Search } from 'lucide-react'
import { api, ApiError } from '../api/client'
import { fmtUSD } from '../lib/money'
import { fmtDate, fmtDateTime } from '../lib/format'
import { useQuery } from '../lib/useQuery'
import { useDebounced } from '../lib/useDebounced'
import type { User } from '../types'
import BudgetPolicyModal from '../components/BudgetPolicyModal'
import ConcurrencyPolicyModal from '../components/ConcurrencyPolicyModal'
import RevealResetURLModal from '../components/RevealResetURLModal'
import {
  SegmentedFilter,
  Button,
  EmptyRow,
  ErrorRow,
  PageHeader,
  SkeletonRows,
  Table,
  Td,
  Tr,
  RowMenu,
  useConfirm,
  useToast,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

export default function Users({ principal }: { principal: Principal }) {
  const [tab] = useSectionTab(['people', 'invites'] as const, 'people')
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)
  const [editingUser, setEditingUser] = useState<User | null>(null)
  const [editingConcurrencyUser, setEditingConcurrencyUser] = useState<User | null>(null)
  const [resetReveal, setResetReveal] = useState<{ email: string; url: string; expires_at: string } | null>(null)
  const [resetting, setResetting] = useState<number | null>(null)
  const [params] = useSearchParams()
  const [q, setQ] = useState(params.get('q') ?? '')
  const [roleFilter, setRoleFilter] = useState<'all' | 'admin' | 'manager' | 'member'>('all')
  const toast = useToast()
  const confirm = useConfirm()
  // Search and role run on the server so they cover every user, not the
  // loaded page; a new query starts from the first page.
  const debouncedQ = useDebounced(q.trim())
  useEffect(() => { setOffset(0) }, [debouncedQ, roleFilter])

  const {
    data: page,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(
    () => api.listUsers({ limit, offset }, { q: debouncedQ, role: roleFilter === 'all' ? '' : roleFilter }),
    [limit, offset, debouncedQ, roleFilter],
  )
  const users = page?.items ?? null
  const total = page?.total ?? 0
  const filtered = users ?? []

  return (
    <>
      <PageHeader
        title="Users"
        description="Everyone with console access."
        actions={
          tab === 'people'
            ? <Link to="?tab=invites&new=1"><Button leadingIcon={<Plus size={13} />}>Invite user</Button></Link>
            : undefined
        }
      />
      <SectionTabs label="User sections" current={tab} items={[
        { id: 'people', label: 'People', count: tab === 'people' ? total : undefined },
        { id: 'invites', label: 'Invitations' },
      ]} />

      {tab === 'invites' ? <Invites principal={principal} embedded /> : <>
      <div className="page-toolbar">
        <div className="page-toolbar-l">
          <label className="search-shell">
            <Search size={13} />
            <input placeholder="Search by email, name…" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
          <SegmentedFilter
            variant="window"
            value={roleFilter}
            onChange={setRoleFilter}
            options={[
              { value: 'all', label: 'All roles' },
              { value: 'admin', label: 'Admin' },
              { value: 'manager', label: 'Manager' },
              { value: 'member', label: 'Member' },
            ]}
          />
        </div>
      </div>

      <Table
        head={['User', 'Team', 'Role', 'Last login', 'Joined', 'Budget', '']}
        footer={
          <Pagination
            total={total}
            limit={limit}
            offset={offset}
            onChange={(p) => {
              setLimit(p.limit)
              setOffset(p.offset)
            }}
          />
        }
      >
        {loading ? (
          <SkeletonRows rows={6} cols={7} />
        ) : error && users === null ? (
          <ErrorRow
            cols={7}
            title="Couldn't load users"
            message={error.message}
            onRetry={reload}
            retrying={refreshing}
          />
        ) : filtered.length === 0 ? (
          <EmptyRow cols={7}>No users match.</EmptyRow>
        ) : (
          filtered.map((u) => (
            <Tr key={u.id}>
              <Td>
                <div className="user-cell">
                  <div className="avatar">{(u.name || u.email).split(/\s+/).map((p) => p[0]).join('').slice(0, 2).toUpperCase()}</div>
                  <div>
                    <div className="user-name">{u.name || u.email}</div>
                    <div className="user-email mono">{u.email}</div>
                  </div>
                </div>
              </Td>
              <Td>
                {u.team_slug ? (
                  <Link to={`/teams/${u.team_slug}`} className="linkish mono">{u.team_slug}</Link>
                ) : (
                  <span className="muted">No team</span>
                )}
              </Td>
              <Td>
                <RoleBadge user={u} />
              </Td>
              <Td className="muted">
                {u.last_login_at ? fmtDateTime(u.last_login_at) : '—'}
              </Td>
              <Td className="muted">
                {fmtDate(u.created_at)}
              </Td>
              <Td>
                <button className="budget-cell linkish-cell" onClick={() => setEditingUser(u)}>
                  <div className="budget-cell-top">
                    {u.usd_limit_cents
                      ? <><span className="tnum">{fmtUSD(u.usd_limit_cents)}</span><span className="muted">/ {u.period}</span></>
                      : <span className="muted">No limit</span>}
                  </div>
                </button>
              </Td>
              <Td align="right">
                <div className="row-actions">
                  <button onClick={() => setEditingUser(u)} className="linkish">Edit</button>
                  <RowMenu label={`Actions for ${u.email}`} items={[
                    { label: 'Concurrency…', onSelect: () => setEditingConcurrencyUser(u) },
                    {
                      label: 'Reset password…',
                      disabled: resetting === u.id,
                      onSelect: () => {
                        void (async () => {
                          setResetting(u.id)
                          try {
                            const r = await api.issuePasswordReset(u.id)
                            setResetReveal({ email: r.email, url: r.url, expires_at: r.expires_at })
                          } catch (e) {
                            toast.error('Could not issue reset', e instanceof ApiError ? e.message : undefined)
                          } finally {
                            setResetting(null)
                          }
                        })()
                      },
                    },
                    {
                      label: u.disabled_at ? 'Re-enable user' : 'Disable user…',
                      destructive: !u.disabled_at,
                      onSelect: () => {
                        void (async () => {
                          const next = !u.disabled_at
                          if (next) {
                            // Disabling cuts off a person's gateway access — the
                            // one destructive action here that had no confirm.
                            const ok = await confirm({
                              title: `Disable ${u.name || u.email}?`,
                              description: 'They will immediately lose access to the gateway and the console until re-enabled.',
                              confirmLabel: 'Disable user',
                              destructive: true,
                            })
                            if (!ok) return
                          }
                          try {
                            await api.setUserDisabled(u.id, next)
                            toast.success(next ? 'User disabled' : 'User re-enabled')
                            reload()
                          } catch (e) {
                            toast.error('Failed', e instanceof ApiError ? e.message : undefined)
                          }
                        })()
                      },
                    },
                  ]} />
                </div>
              </Td>
            </Tr>
          ))
        )}
      </Table>

      {resetReveal && (
        <RevealResetURLModal
          data={resetReveal}
          onClose={() => setResetReveal(null)}
        />
      )}

      {editingUser && (
        <BudgetPolicyModal
          title="User budget policy"
          subject={editingUser.name ? `${editingUser.name} · ${editingUser.email}` : editingUser.email}
          currentLimitCents={editingUser.usd_limit_cents}
          currentPeriod={editingUser.period}
          onClose={() => setEditingUser(null)}
          onSave={async (body) => {
            await api.updateUserBudget(editingUser.id, body)
            setEditingUser(null)
            toast.success('Budget updated')
            reload()
          }}
        />
      )}
      {editingConcurrencyUser && (
        <ConcurrencyPolicyModal
          title="User concurrency policy"
          subject={editingConcurrencyUser.name ? `${editingConcurrencyUser.name} · ${editingConcurrencyUser.email}` : editingConcurrencyUser.email}
          current={editingConcurrencyUser.max_parallel_requests}
          onClose={() => setEditingConcurrencyUser(null)}
          onSave={async (value) => {
            await api.updateUserConcurrency(editingConcurrencyUser.id, value)
            setEditingConcurrencyUser(null)
            toast.success('User concurrency updated')
            reload()
          }}
        />
      )}
      </>}
    </>
  )
}

// RoleBadge renders the three real roles (admin/manager/member) with
// distinct tones. Falls back to is_admin when the role string is missing
// (legacy responses from a pre-Role server).
function RoleBadge({ user }: { user: User }) {
  const role = user.role || (user.is_admin ? 'admin' : 'member')
  return <span className="role-text">{role.charAt(0).toUpperCase() + role.slice(1)}</span>
}

