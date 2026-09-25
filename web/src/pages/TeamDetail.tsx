import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import SectionTabs, { useSectionTab } from '../components/SectionTabs'
import { CheckCircle2, KeyRound, Plus, RefreshCcw } from 'lucide-react'
import { api, ApiError } from '../api/client'
import { principalIsAdmin, type Principal } from '../auth'
import type { Team, ApiKey, CreateInviteResponse, CreateKeyResponse, EffectivePolicy, TeamModel, PolicyLayer, ServiceAccount, TeamMember } from '../types'
import BudgetPolicyModal from '../components/BudgetPolicyModal'
import ConcurrencyPolicyModal from '../components/ConcurrencyPolicyModal'
import RatesPolicyModal from '../components/RatesPolicyModal'
import TeamModelsModal from '../components/TeamModelsModal'
import CustomersPanel from '../components/CustomersPanel'
import IssueKeyModal from '../components/IssueKeyModal'
import EditKeyModal from '../components/EditKeyModal'
import KeyBudgetModal from '../components/KeyBudgetModal'
import KeyBudgetField from '../components/KeyBudgetField'
import { keyBudgetUSD, parseKeyBudgetUSD } from '../lib/keyBudget'
import { fmtUSD } from '../lib/money'
import RevealKeyModal from '../components/RevealKeyModal'
import CreateInviteModal from '../components/CreateInviteModal'
import RevealInviteModal from '../components/RevealInviteModal'
import {
  Badge,
  Button,
  EmptyRow,
  Field,
  Input,
  MetricStrip,
  Modal,
  PageHeader,
  StatTile,
  Table,
  Td,
  Tr,
  RowMenu,
  useConfirm,
  useToast,
  Select,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

const teamTabs = ['overview', 'keys', 'service_accounts', 'members', 'settings', 'customers', 'limits'] as const

export default function TeamDetail({ principal }: { principal: Principal }) {
  const isAdmin = principalIsAdmin(principal)
  const { slug } = useParams<{ slug: string }>()
  // Remount on team changes so data/modals from a previous team cannot linger.
  return slug ? <TeamWorkspace key={slug} slug={slug} isAdmin={isAdmin} principal={principal} /> : null
}

function TeamWorkspace({ slug, isAdmin, principal }: { slug: string; isAdmin: boolean; principal: Principal }) {
  const requestVersion = useRef(0)
  useEffect(() => () => { requestVersion.current++ }, [])
  const [tab] = useSectionTab(teamTabs, 'overview')
  const [team, setTeam] = useState<Team | null>(null)
  const [keys, setKeys] = useState<ApiKey[] | null>(null)
  const [keysTotal, setKeysTotal] = useState(0)
  const [keysLimit, setKeysLimit] = useState(50)
  const [keysOffset, setKeysOffset] = useState(0)
  const [aliases, setAliases] = useState<TeamModel[]>([])
  const [aliasesTotal, setAliasesTotal] = useState(0)
  const [teamUsers, setTeamUsers] = useState<TeamMember[]>([])
  const [membersTotal, setMembersTotal] = useState(0)
  const [membersLimit, setMembersLimit] = useState(50)
  const [membersOffset, setMembersOffset] = useState(0)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [issuing, setIssuing] = useState(false)
  const [editingKey, setEditingKey] = useState<ApiKey | null>(null)
  const [policyKey, setPolicyKey] = useState<ApiKey | null>(null)
  const [revealKey, setRevealKey] = useState<CreateKeyResponse | null>(null)
  const [budgetKey, setBudgetKey] = useState<ApiKey | null>(null)
  const [editingBudget, setEditingBudget] = useState(false)
  const [editingConcurrency, setEditingConcurrency] = useState(false)
  const [editingRates, setEditingRates] = useState(false)
  const [editingModels, setEditingModels] = useState(false)
  const [inviting, setInviting] = useState(false)
  const [inviteReveal, setInviteReveal] = useState<CreateInviteResponse | null>(null)
  const confirm = useConfirm()
  const toast = useToast()

  const load = async () => {
    if (!slug) return
    const version = ++requestVersion.current
    try {
      const [t, k, a, users] = await Promise.all([
        api.getTeam(slug),
        api.listKeys(slug, { limit: keysLimit, offset: keysOffset }),
        api.listTeamModels(slug, { limit: 500 }),
        api.listTeamMembers(slug, { limit: membersLimit, offset: membersOffset }),
      ])
      if (version !== requestVersion.current) return
      setTeam(t)
      setKeys(k.items)
      setKeysTotal(k.total)
      setAliases(a.items)
      setAliasesTotal(a.total)
      setTeamUsers(users.items)
      setMembersTotal(users.total)
      setLoadError(null)
    } catch (e) {
      if (version !== requestVersion.current) return
      setLoadError(e instanceof ApiError ? e.message : 'Could not load team data.')
      toast.error('Failed to load team', e instanceof ApiError ? e.message : undefined)
    }
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug, keysLimit, keysOffset, membersLimit, membersOffset])

  const [rotatingKey, setRotatingKey] = useState<ApiKey | null>(null)
  const [selectedKeys, setSelectedKeys] = useState<Set<number>>(new Set())

  const revoke = async (k: ApiKey) => {
    const ok = await confirm({
      title: `Revoke key ${k.prefix}…?`,
      description: 'Existing sessions using this key will start failing immediately.',
      confirmLabel: 'Revoke',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.revokeKey(k.id)
      toast.success('Key revoked')
      load()
    } catch (e) {
      toast.error('Revoke failed', e instanceof ApiError ? e.message : undefined)
    }
  }

  const rotate = (key: ApiKey) => setRotatingKey(key)

  const setPaused = async (k: ApiKey, paused: boolean) => {
    try {
      await (paused ? api.pauseKey(k.id) : api.resumeKey(k.id))
      toast.success(paused ? `Key ${k.prefix}… paused` : `Key ${k.prefix}… resumed`)
      load()
    } catch (e) {
      toast.error(paused ? 'Couldn’t pause the key' : 'Couldn’t resume the key', e instanceof ApiError ? e.message : undefined)
    }
  }

  // Bulk actions apply one request per key and report partial failures,
  // so a single refusal doesn't hide what did change.
  const bulk = async (action: 'pause' | 'resume' | 'revoke') => {
    const targets = (keys ?? []).filter((k) => selectedKeys.has(k.id))
    if (targets.length === 0) return
    if (action === 'revoke') {
      const ok = await confirm({
        title: `Revoke ${targets.length} key${targets.length === 1 ? '' : 's'}?`,
        description: 'Applications using these keys start failing immediately. Revocation can’t be undone.',
        confirmLabel: 'Revoke',
        destructive: true,
      })
      if (!ok) return
    }
    const call = { pause: api.pauseKey, resume: api.resumeKey, revoke: api.revokeKey }[action]
    const results = await Promise.allSettled(targets.map((k) => call(k.id)))
    const failed = results.filter((r) => r.status === 'rejected').length
    const done = targets.length - failed
    const verb = { pause: 'Paused', resume: 'Resumed', revoke: 'Revoked' }[action]
    if (failed === 0) toast.success(`${verb} ${done} key${done === 1 ? '' : 's'}`)
    else toast.error(`${verb} ${done} of ${targets.length} keys`, `${failed} could not be changed.`)
    setSelectedKeys(new Set())
    load()
  }

  if (loadError) return <section className="card" role="alert">
    <h1>Team unavailable</h1><p>{loadError}</p>
    <Button onClick={load}>Retry</Button>
  </section>
  if (!team) return <p role="status">Loading team…</p>

  const activeKeys = (keys ?? []).filter((k) => keyStatus(k) === 'active').length

  return (
    <>
      <PageHeader
        crumbs={isAdmin ? [{ label: 'Teams', to: '/teams' }] : undefined}
        title={team?.name ?? slug}
        description={<span className="mono">{team?.slug ?? slug}</span>}
        actions={
          <>
            <Button variant="ghost" leadingIcon={<RefreshCcw size={13} />} onClick={load}>Refresh</Button>
            {tab === 'keys' && <Button leadingIcon={<Plus size={13} />} onClick={() => setIssuing(true)}>Issue key</Button>}
            {tab === 'members' && <Button leadingIcon={<Plus size={13} />} onClick={() => setInviting(true)}>Invite member</Button>}
          </>
        }
      />

      {team && tab === 'overview' && (
        <MetricStrip>
          <StatTile label="Rate limit" value={team.rpm ?? 'unlimited'} hint={team.rpm ? 'req/min' : undefined} />
          <StatTile label="Concurrency" value={team.max_parallel_requests ?? 'unlimited'} hint={team.max_parallel_requests ? 'distributed in-flight' : undefined} />
          <StatTile
            label="Budget"
            value={team.usd_limit_cents != null ? fmtUSD(team.usd_limit_cents) : 'unlimited'}
            hint={team.usd_limit_cents != null ? `per ${team.period}` : undefined}
            tone={team.usd_limit_cents != null ? 'accent' : 'neutral'}
          />
          <StatTile label="Keys" value={keysTotal} hint={keysTotal === 0 ? 'none issued yet' : `${activeKeys} active on this page`} />
          <StatTile label="Members" value={membersTotal} />
        </MetricStrip>
      )}

      <SectionTabs label="Team sections" current={tab} items={[
        { id: 'overview', label: 'Overview' },
        { id: 'keys', label: 'Virtual keys', count: keysTotal },
        { id: 'members', label: 'Members' },
        { id: 'service_accounts', label: 'Service accounts' },
        { id: 'customers', label: 'Customers' },
        { id: 'limits', label: 'Budget & limits' },
        { id: 'settings', label: 'Data privacy' },
      ]} />

      {tab === 'limits' && team && <div className="page-section">
        <div className="scope-notice"><div><strong>Team policy · {team.name}</strong><p>Budget and concurrency caps apply to requests made with this team's keys. Individual keys can impose additional limits.</p></div></div>
        <div className="workspace-grid">
          <section className="card"><h2 className="card-title">Spending budget</h2>
            <p className="policy-value">{team.usd_limit_cents != null ? fmtUSD(team.usd_limit_cents) : 'Unlimited'} <span className="muted small">per {team.period}</span></p>
            <p className="muted">Control the spending ceiling for this team.</p>
            <Button variant="ghost" onClick={() => setEditingBudget(true)}>Edit budget</Button>
          </section>
          <section className="card"><h2 className="card-title">Concurrent requests</h2>
            <p className="policy-value">{team.max_parallel_requests ?? 'Unlimited'}</p>
            <p className="muted">A distributed in-flight limit shared by this team's keys.</p>
            <Button variant="ghost" onClick={() => setEditingConcurrency(true)}>Edit concurrency</Button>
          </section>
          <section className="card"><h2 className="card-title">Request & token rates</h2>
            <p>{team.rpm != null ? team.rpm.toLocaleString() : 'Unlimited'} requests / minute</p><p>{team.tpm != null ? team.tpm.toLocaleString() : 'Unlimited'} tokens / minute</p>
            <p className="muted">Per-minute caps shared by every key in this team.</p>
            <Button variant="ghost" onClick={() => setEditingRates(true)}>Edit rate limits</Button>
          </section>
          <section className="card"><h2 className="card-title">Model access</h2>
            <p className="policy-value">{!team.allowed_models || team.allowed_models.includes('*') ? 'All models' : `${team.allowed_models.length} model${team.allowed_models.length === 1 ? '' : 's'}`}</p>
            {team.allowed_models && !team.allowed_models.includes('*') && (
              <p className="mono small" style={{ overflowWrap: 'anywhere' }}>{team.allowed_models.join(', ')}</p>
            )}
            <p className="muted">Key model restrictions can only narrow the team's model access.</p>
            {isAdmin
              ? <Button variant="ghost" onClick={() => setEditingModels(true)}>Edit model access</Button>
              : <Link className="linkish" to="?tab=keys">Review virtual keys</Link>}
          </section>
        </div>
      </div>}

      {tab === 'customers' && <CustomersPanel key={slug} slug={slug} />}
      {tab === 'overview' && (
        <section className="card">
          <h2 className="card-title">{keysTotal === 0 || membersTotal === 0 ? 'Finish setting up this team' : 'Manage this team'}</h2>
          <ul className="next-steps">
            <li data-done={keysTotal > 0 ? 'true' : undefined}>
              <div><strong>Issue a virtual key</strong><p>Applications call the gateway with a team key.</p></div>
              <Link className="btn btn-ghost btn-size-sm" to="?tab=keys">{keysTotal > 0 ? `View ${keysTotal} key${keysTotal === 1 ? '' : 's'}` : 'Issue key'}</Link>
            </li>
            <li data-done={membersTotal > 0 ? 'true' : undefined}>
              <div><strong>Add members</strong><p>Invite people who should manage keys and see usage.</p></div>
              <Link className="btn btn-ghost btn-size-sm" to="?tab=members">{membersTotal > 0 ? `View ${membersTotal} member${membersTotal === 1 ? '' : 's'}` : 'Add members'}</Link>
            </li>
            <li data-done={team.usd_limit_cents != null ? 'true' : undefined}>
              <div><strong>Set a budget and limits</strong><p>Cap monthly spend, request rates and concurrency.</p></div>
              <Link className="btn btn-ghost btn-size-sm" to="?tab=limits">Budget & limits</Link>
            </li>
          </ul>
        </section>
      )}

      {tab === 'keys' && aliasesTotal > aliases.length && <p role="status">Model picker shows the first {aliases.length} of {aliasesTotal} allowed models. Unrestricted keys still inherit the complete team policy.</p>}
      {tab === 'keys' && (
        <Table
          head={[
            <input
              key="all"
              type="checkbox"
              aria-label="Select all keys on this page"
              checked={(keys ?? []).some((k) => keyStatus(k) !== 'revoked') && (keys ?? []).filter((k) => keyStatus(k) !== 'revoked').every((k) => selectedKeys.has(k.id))}
              onChange={(e) => setSelectedKeys(e.target.checked ? new Set((keys ?? []).filter((k) => keyStatus(k) !== 'revoked').map((k) => k.id)) : new Set())}
            />,
            'Prefix', 'Label', 'Owner', 'Models', 'Limits', 'Expires', 'Status', '',
          ]}
          header={
            <div className="data-head">
              <h2 className="data-h">
                Keys <span className="data-count tnum">{(keys ?? []).length}</span>
              </h2>
              {selectedKeys.size > 0 && (
                <div className="bulk-bar" role="toolbar" aria-label="Selected keys">
                  <span className="tnum">{selectedKeys.size} selected</span>
                  <Button size="sm" variant="ghost" onClick={() => void bulk('pause')}>Pause</Button>
                  <Button size="sm" variant="ghost" onClick={() => void bulk('resume')}>Resume</Button>
                  <Button size="sm" variant="ghost" className="is-danger" onClick={() => void bulk('revoke')}>Revoke</Button>
                  <button type="button" className="linkish" onClick={() => setSelectedKeys(new Set())}>Clear</button>
                </div>
              )}
            </div>
          }
          footer={
            <Pagination
              total={keysTotal}
              limit={keysLimit}
              offset={keysOffset}
              onChange={(p) => {
                setKeysLimit(p.limit)
                setKeysOffset(p.offset)
              }}
            />
          }
        >
          {keys === null ? (
            <SkeletonRows cols={9} />
          ) : keys.length === 0 ? (
            <EmptyRow cols={9}>
              <KeyRound size={18} />
              <span>No keys yet.</span>
            </EmptyRow>
          ) : (
            keys.map((k) => (
              <Tr key={k.id}>
                <Td>
                  {keyStatus(k) !== 'revoked' && (
                    <input
                      type="checkbox"
                      aria-label={`Select key ${k.prefix}`}
                      checked={selectedKeys.has(k.id)}
                      onChange={(e) => setSelectedKeys((prev) => {
                        const next = new Set(prev)
                        if (e.target.checked) next.add(k.id)
                        else next.delete(k.id)
                        return next
                      })}
                    />
                  )}
                </Td>
                <Td mono>
                  <span className="cell-prefix" title={k.prefix}>{k.prefix}…</span>
                  {k.rotated_from_key_id && <Badge tone="accent" monospace={false} className="ml-2">rotated</Badge>}
                </Td>
                <Td>{k.name || <span className="muted">—</span>}</Td>
                <Td className="muted mono text-[11px]"><span className="cell-owner" title={renderOwner(k)}>{renderOwner(k)}</span></Td>
                <Td className="mono muted text-[11px]">{renderAllowed(k.allowed_models)}</Td>
                <Td className="muted text-[11px]"><span className="cell-limits">{renderLimits(k)}</span></Td>
                <Td className="muted">
                  {k.expires_at ? new Date(k.expires_at).toLocaleString() : 'never'}
                </Td>
                <Td>{renderStatus(k)}</Td>
                <Td align="right">
                  <div className="row-actions">
                    {keyStatus(k) !== 'revoked' && (
                      <button className="linkish" onClick={() => setEditingKey(k)}>Edit</button>
                    )}
                    <RowMenu label={`Actions for key ${k.prefix}`} items={[
                      { label: 'Budget…', onSelect: () => setBudgetKey(k) },
                      ...(isAdmin ? [{ label: 'Effective policy…', onSelect: () => setPolicyKey(k) }] : []),
                      ...(keyStatus(k) === 'active' ? [{ label: 'Rotate…', onSelect: () => rotate(k) }] : []),
                      ...(keyStatus(k) === 'active' ? [{ label: 'Pause', onSelect: () => void setPaused(k, true) }] : []),
                      ...(keyStatus(k) === 'paused' ? [{ label: 'Resume', onSelect: () => void setPaused(k, false) }] : []),
                      ...(keyStatus(k) !== 'revoked' ? [{ label: 'Revoke…', destructive: true, onSelect: () => void revoke(k) }] : []),
                    ]} />
                  </div>
                </Td>
              </Tr>
            ))
          )}
        </Table>
      )}

      {tab === 'service_accounts' && team && (
        <ServiceAccountsTab
          isAdmin={isAdmin}
          slug={team.slug}
          aliases={aliases.map((a) => a.alias)}
          onRevealKey={(resp) => setRevealKey(resp)}
        />
      )}

      {tab === 'settings' && team && (
        <PrivacyPanel
          isAdmin={isAdmin}
          team={team}
          onChange={(next) => setTeam((t) => (t ? { ...t, capture_payloads: next } : t))}
        />
      )}

      {tab === 'members' && (
        <Table
          head={['User', 'Email', 'Role', 'Last login']}
          footer={<Pagination total={membersTotal} limit={membersLimit} offset={membersOffset} onChange={(p) => { setMembersLimit(p.limit); setMembersOffset(p.offset) }} />}
          header={
            <div className="data-head">
              <h2 className="data-h">
                Members <span className="data-count tnum">{membersTotal}</span>
              </h2>
            </div>
          }
        >
          {teamUsers.length === 0 ? (
            <EmptyRow cols={4}>No members yet.</EmptyRow>
          ) : (
            teamUsers.map((u) => (
              <Tr key={u.id}>
                <Td>{u.name || <span className="muted">—</span>}{u.disabled_at && <Badge tone="warning">Disabled</Badge>}</Td>
                <Td className="mono">{u.email}</Td>
                <Td>
                  {(() => {
                    const role = u.role
                    const tone = role === 'admin' ? 'accent' : role === 'manager' ? 'warning' : 'neutral'
                    return (
                      <Badge tone={tone} monospace={false}>{role}</Badge>
                    )
                  })()}
                </Td>
                <Td className="muted">
                  {u.last_login_at ? new Date(u.last_login_at).toLocaleString() : '—'}
                </Td>
              </Tr>
            ))
          )}
        </Table>
      )}

      {issuing && team && (
        <IssueKeyModal
          slug={team.slug}
          availableAliases={aliases.map((a) => a.alias)}
          onClose={() => setIssuing(false)}
          onIssued={(resp) => {
            setIssuing(false)
            setRevealKey(resp)
            toast.success('Key issued')
            load()
          }}
        />
      )}
      {editingKey && (
        <EditKeyModal
          apiKey={editingKey}
          availableAliases={aliases.map((a) => a.alias)}
          onClose={() => setEditingKey(null)}
          onSaved={() => {
            setEditingKey(null)
            toast.success('Key updated')
            load()
          }}
        />
      )}
      {policyKey && (
        <EffectivePolicyModal apiKey={policyKey} onClose={() => setPolicyKey(null)} />
      )}
      {budgetKey && <KeyBudgetModal apiKey={budgetKey} onClose={() => setBudgetKey(null)} onSaved={() => { setBudgetKey(null); toast.success('Key budget updated'); load() }} />}
      {editingBudget && team && (
        <BudgetPolicyModal
          title="Team budget policy"
          subject={`${team.name} (${team.slug})`}
          currentLimitCents={team.usd_limit_cents}
          currentPeriod={team.period}
          onClose={() => setEditingBudget(false)}
          onSave={async (body) => {
            const next = await api.updateTeamBudget(team.slug, body)
            setTeam(next)
            setEditingBudget(false)
            toast.success('Budget updated')
          }}
        />
      )}
      {editingConcurrency && team && (
        <ConcurrencyPolicyModal
          title="Team concurrency policy"
          subject={`${team.name} (${team.slug})`}
          current={team.max_parallel_requests}
          onClose={() => setEditingConcurrency(false)}
          onSave={async (value) => {
            const next = await api.updateTeamConcurrency(team.slug, value)
            setTeam(next)
            setEditingConcurrency(false)
            toast.success('Concurrency limit updated')
          }}
        />
      )}
      {editingRates && team && (
        <RatesPolicyModal
          title="Team rate limits"
          subject={`${team.name} (${team.slug})`}
          currentRPM={team.rpm}
          currentTPM={team.tpm}
          onClose={() => setEditingRates(false)}
          onSave={async (body) => {
            const next = await api.updateTeamRates(team.slug, body)
            setTeam(next)
            setEditingRates(false)
            toast.success('Rate limits updated')
          }}
        />
      )}
      {editingModels && team && (
        <TeamModelsModal
          subject={`${team.name} (${team.slug})`}
          current={team.allowed_models ?? ['*']}
          onClose={() => setEditingModels(false)}
          onSave={async (models) => {
            const next = await api.updateTeamAllowedModels(team.slug, models)
            setTeam(next)
            setEditingModels(false)
            toast.success('Model access updated')
          }}
        />
      )}
      {revealKey && <RevealKeyModal resp={revealKey} onClose={() => setRevealKey(null)} />}
      {rotatingKey && (
        <RotateKeyModal
          apiKey={rotatingKey}
          onClose={() => setRotatingKey(null)}
          onRotated={(resp) => { setRotatingKey(null); setRevealKey(resp); toast.success('Key rotated'); load() }}
        />
      )}
      {inviting && (
        <CreateInviteModal
          principal={principal}
          team={slug}
          onClose={() => setInviting(false)}
          onCreated={(resp) => {
            setInviting(false)
            setInviteReveal(resp)
            toast.success('Invite created')
          }}
        />
      )}
      {inviteReveal && <RevealInviteModal resp={inviteReveal} onClose={() => setInviteReveal(null)} />}
    </>
  )
}

function keyStatus(key: ApiKey): 'active' | 'paused' | 'expired' | 'revoked' {
  if (key.revoked_at) return 'revoked'
  if (key.expires_at && new Date(key.expires_at).getTime() <= Date.now()) return 'expired'
  if (key.paused_at) return 'paused'
  return 'active'
}

function renderStatus(k: ApiKey) {
  const s = keyStatus(k)
  if (s === 'revoked') return <Badge tone="neutral" monospace={false}>revoked</Badge>
  if (s === 'expired') return <Badge tone="warning" monospace={false}>expired</Badge>
  if (s === 'paused') return <Badge tone="warning" monospace={false}>paused</Badge>
  return <span className="pill pill-ok"><CheckCircle2 size={11} /> active</span>
}

function renderAllowed(allowed: string[]) {
  if (!allowed || allowed.length === 0 || allowed.includes('*')) return 'all team models'
  return allowed.join(', ')
}

function renderOwner(key: ApiKey) {
  if (key.service_account_id) return key.service_account_name || `service account #${key.service_account_id}`
  if (!key.user_id) return 'shared team'
  if (key.owner_user_name && key.owner_user_email) return `${key.owner_user_name} · ${key.owner_user_email}`
  return key.owner_user_email || key.owner_user_name || `user #${key.user_id}`
}

function renderLimits(key: ApiKey) {
  const parts = []
  if (key.usd_limit_cents && key.usd_limit_cents > 0) parts.push(`$${keyBudgetUSD(key.usd_limit_cents)} / team period`)
  if (key.rpm && key.rpm > 0) parts.push(`${key.rpm} rpm`)
  if (key.tpm && key.tpm > 0) parts.push(`${key.tpm} tpm`)
  if (key.max_parallel_requests && key.max_parallel_requests > 0) parts.push(`${key.max_parallel_requests} concurrent`)
  if (parts.length === 0) return 'team defaults'
  return parts.join(' · ')
}

function ServiceAccountsTab({
  isAdmin,
  slug,
  aliases,
  onRevealKey,
}: {
  isAdmin: boolean
  slug: string
  aliases: string[]
  onRevealKey: (resp: CreateKeyResponse) => void
}) {
  const [sas, setSas] = useState<ServiceAccount[] | null>(null)
  const [creating, setCreating] = useState(false)
  const [issuingFor, setIssuingFor] = useState<ServiceAccount | null>(null)
  const [editingConcurrency, setEditingConcurrency] = useState<ServiceAccount | null>(null)
  const toast = useToast()
  const confirm = useConfirm()

  const load = () => {
    api
      .listServiceAccounts(slug, { limit: 200 })
      .then((page) => setSas(page.items))
      .catch((e) => toast.error('Failed to load service accounts', e instanceof ApiError ? e.message : undefined))
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug])

  const archive = async (sa: ServiceAccount) => {
    const ok = await confirm({
      title: `Archive service account "${sa.name}"?`,
      description: 'Existing keys keep working until revoked. The SA stops appearing in admission and the list.',
      confirmLabel: 'Archive',
      destructive: true,
    })
    if (!ok) return
    try {
      await api.archiveServiceAccount(sa.id)
      toast.success('Service account archived')
      load()
    } catch (e) {
      toast.error('Failed to archive', e instanceof ApiError ? e.message : undefined)
    }
  }

  return (
    <>
      {!isAdmin && <p className="scope-notice">You can register service accounts for your team. An administrator manages their keys, concurrency and archival.</p>}
      <Table
        head={['Name', 'Description', <span className="cell-num" key="b">Budget</span>, 'Limits', 'Created', '']}
        header={
          <div className="data-head">
            <h2 className="data-h">
              Service accounts <span className="data-count tnum">{(sas ?? []).length}</span>
            </h2>
            <span className="muted">Non-human principals — CI/CD, scripts, server-to-server.</span>
            <div className="data-head-r">
              <Button leadingIcon={<Plus size={13} />} onClick={() => setCreating(true)}>
                New service account
              </Button>
            </div>
          </div>
        }
      >
        {sas === null ? (
          <SkeletonRows cols={6} />
        ) : sas.length === 0 ? (
          <EmptyRow cols={6}>
            <KeyRound size={18} />
            <span>No service accounts yet.</span>
          </EmptyRow>
        ) : (
          sas.map((sa) => (
            <Tr key={sa.id}>
              <Td mono>{sa.name}</Td>
              <Td className="muted small">{sa.description || <span className="muted">—</span>}</Td>
              <Td num mono>
                {sa.usd_limit_cents != null
                  ? `$${(sa.usd_limit_cents / 100).toFixed(2)}/${sa.period}`
                  : <span className="muted">team default</span>}
              </Td>
              <Td className="muted">
                {sa.rpm || sa.tpm || sa.max_parallel_requests
                  ? [sa.rpm && `${sa.rpm} rpm`, sa.tpm && `${sa.tpm} tpm`, sa.max_parallel_requests && `${sa.max_parallel_requests} concurrent`].filter(Boolean).join(' · ')
                  : '—'}
              </Td>
              <Td className="muted">{new Date(sa.created_at).toLocaleDateString()}</Td>
              <Td align="right">
                {isAdmin && <div className="row-actions">
                  <button className="linkish" onClick={() => setIssuingFor(sa)}>Issue key</button>
                  <button className="linkish" onClick={() => setEditingConcurrency(sa)}>Concurrency</button>
                  <button
                    className="linkish"
                    style={{ color: 'var(--danger-fg)' }}
                    onClick={() => archive(sa)}
                  >
                    Archive
                  </button>
                </div>}
              </Td>
            </Tr>
          ))
        )}
      </Table>

      {creating && (
        <CreateServiceAccountModal
          slug={slug}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            toast.success('Service account created')
            load()
          }}
        />
      )}

      {issuingFor && (
        <IssueServiceAccountKeyModal
          sa={issuingFor}
          aliases={aliases}
          onClose={() => setIssuingFor(null)}
          onIssued={(resp) => {
            setIssuingFor(null)
            onRevealKey(resp)
            toast.success('Key issued')
          }}
        />
      )}
      {editingConcurrency && (
        <ConcurrencyPolicyModal
          title="Service account concurrency"
          subject={editingConcurrency.name}
          current={editingConcurrency.max_parallel_requests}
          onClose={() => setEditingConcurrency(null)}
          onSave={async (value) => {
            await api.updateServiceAccountConcurrency(editingConcurrency.id, value)
            setEditingConcurrency(null)
            toast.success('Service account concurrency updated')
            load()
          }}
        />
      )}
    </>
  )
}

function CreateServiceAccountModal({
  slug,
  onClose,
  onCreated,
}: {
  slug: string
  onClose: () => void
  onCreated: () => void
}) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [usdLimit, setUsdLimit] = useState('')
  const [period, setPeriod] = useState('month')
  const [rpm, setRpm] = useState('')
  const [tpm, setTpm] = useState('')
  const [maxParallel, setMaxParallel] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null)
    setBusy(true)
    try {
      await api.createServiceAccount(slug, {
        name: name.trim(),
        description: description.trim() || undefined,
        usd_limit_cents: usdLimit ? Math.round(parseFloat(usdLimit) * 100) : null,
        period,
        rpm: rpm ? Number(rpm) : null,
        tpm: tpm ? Number(tpm) : null,
        max_parallel_requests: maxParallel ? Number(maxParallel) : null,
      })
      onCreated()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'Could not create')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="New service account" onClose={onClose}>
      <form onSubmit={submit} className="form-grid" style={{ marginTop: 8 }}>
        <Field label="Name" required hint="Unique within the team. Used in audit logs.">
          <Input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="ci-runner" />
        </Field>
        <Field label="Description" hint="Optional. Helps operators identify what uses this SA.">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="GitHub Actions main workflow" />
        </Field>
        <div className="form-grid form-grid-2">
          <Field label="USD limit ($)" hint="Per period. Leave blank for team default.">
            <Input type="number" step="0.01" min="0" value={usdLimit} onChange={(e) => setUsdLimit(e.target.value)} placeholder="50.00" />
          </Field>
          <Field label="Period">
            <select className="text-input" value={period} onChange={(e) => setPeriod(e.target.value)}>
              <option value="month">month</option>
              <option value="week">week</option>
              <option value="day">day</option>
            </select>
          </Field>
        </div>
        <div className="form-grid form-grid-2">
          <Field label="RPM cap" hint="Optional. Requests per minute.">
            <Input type="number" min="0" value={rpm} onChange={(e) => setRpm(e.target.value)} placeholder="—" />
          </Field>
          <Field label="TPM cap" hint="Optional. Tokens per minute.">
            <Input type="number" min="0" value={tpm} onChange={(e) => setTpm(e.target.value)} placeholder="—" />
          </Field>
        </div>
        <Field label="Concurrent request cap" hint="Optional distributed cap for every key owned by this service account.">
          <Input type="number" min="1" value={maxParallel} onChange={(e) => setMaxParallel(e.target.value)} placeholder="—" />
        </Field>
        {err && <div className="muted small" style={{ color: 'var(--danger-fg)' }}>{err}</div>}
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose} type="button">Cancel</Button>
          <Button type="submit" loading={busy} disabled={!name.trim()}>Create</Button>
        </div>
      </form>
    </Modal>
  )
}

function IssueServiceAccountKeyModal({
  sa,
  aliases,
  onClose,
  onIssued,
}: {
  sa: ServiceAccount
  aliases: string[]
  onClose: () => void
  onIssued: (resp: CreateKeyResponse) => void
}) {
  const [name, setName] = useState('')
  const [allowed, setAllowed] = useState<string[]>([])
  const [rpm, setRpm] = useState('')
  const [tpm, setTpm] = useState('')
  const [maxParallel, setMaxParallel] = useState('')
  const [usdLimit, setUsdLimit] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      const resp = await api.createServiceAccountKey(sa.id, {
        name: name.trim() || undefined,
        allowed_models: allowed.length > 0 ? allowed : undefined,
        rpm: rpm ? Number(rpm) : null,
        tpm: tpm ? Number(tpm) : null,
        max_parallel_requests: maxParallel ? Number(maxParallel) : null,
        usd_limit_cents: parseKeyBudgetUSD(usdLimit),
      })
      onIssued(resp)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Could not issue key')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={`Issue key — ${sa.name}`} onClose={onClose}>
      <form onSubmit={submit} className="form-grid" style={{ marginTop: 8 }}>
        <Field label="Label" hint="Optional. Helps you find this key later.">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="prod-deploy" />
        </Field>
        <Field label="Allowed models" hint="Leave empty for team default.">
          <select
            multiple
            value={allowed}
            onChange={(e) => setAllowed(Array.from(e.target.selectedOptions).map((o) => o.value))}
            className="text-input"
            style={{ minHeight: 80 }}
          >
            {aliases.map((a) => (
              <option key={a} value={a}>{a}</option>
            ))}
          </select>
        </Field>
        <div className="form-grid form-grid-2">
          <Field label="RPM cap">
            <Input type="number" min="0" value={rpm} onChange={(e) => setRpm(e.target.value)} placeholder="—" />
          </Field>
          <Field label="TPM cap">
            <Input type="number" min="0" value={tpm} onChange={(e) => setTpm(e.target.value)} placeholder="—" />
          </Field>
        </div>
        <Field label="Concurrent request cap" hint="Optional distributed per-key cap. Also subject to the team cap.">
          <Input type="number" min="1" value={maxParallel} onChange={(e) => setMaxParallel(e.target.value)} placeholder="—" />
        </Field>
        <KeyBudgetField value={usdLimit} onChange={setUsdLimit} />
        {err && <div className="muted small" style={{ color: 'var(--danger-fg)' }}>{err}</div>}
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose} type="button">Cancel</Button>
          <Button type="submit" loading={busy}>Issue key</Button>
        </div>
      </form>
    </Modal>
  )
}

function PrivacyPanel({
  isAdmin,
  team,
  onChange,
}: {
  isAdmin: boolean
  team: Team
  onChange: (capture: boolean) => void
}) {
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const confirm = useConfirm()
  const enabled = !!team.capture_payloads

  const toggle = async () => {
    // Turning capture ON has privacy implications; require an explicit
    // confirm. Turning it off can be silent.
    if (!enabled) {
      const ok = await confirm({
        title: 'Capture request bodies?',
        description:
          'Future /v1 calls for this team will store the full prompt and response in the database. ' +
          'Disable any time. Existing rows are not retroactively captured.',
        confirmLabel: 'Enable capture',
      })
      if (!ok) return
    }
    setBusy(true)
    try {
      const next = !enabled
      await api.setTeamCapturePayloads(team.slug, next)
      toast.success(next ? 'Body capture enabled' : 'Body capture disabled')
      onChange(next)
    } catch (e) {
      toast.error('Failed to update', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="data-card" style={{ padding: 18 }}>
      <h2 className="data-h">Privacy</h2>
      <p className="muted small" style={{ marginTop: 8, marginBottom: 14 }}>
        Per-team controls for sensitive data captured by the gateway. These settings only affect
        traffic for team <code className="mono">{team.slug}</code>.
      </p>

      <div className="kv-row" style={{ alignItems: 'flex-start', borderTop: 0, paddingTop: 0 }}>
        <div style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
            <strong>Capture request bodies</strong>
            {enabled ? (
              <Badge tone="accent" monospace={false}>on</Badge>
            ) : (
              <Badge tone="neutral" monospace={false}>off</Badge>
            )}
          </div>
          <p className="muted small" style={{ margin: 0 }}>
            When on, /v1 calls store the full prompt and response in <code className="mono">usage_log_payloads</code>.
            Detail rows under <strong>Usage</strong> show the bodies; turn it off to stop capture.
            Off by default — bodies often contain PII, so we don't store them implicitly.
          </p>
        </div>
        {isAdmin ? <Button onClick={toggle} loading={busy} variant={enabled ? 'ghost' : 'primary'}>
          {enabled ? 'Disable' : 'Enable'}
        </Button> : <span className="muted">Managed by an administrator</span>}
      </div>
    </section>
  )
}

function EffectivePolicyModal({ apiKey, onClose }: { apiKey: ApiKey; onClose: () => void }) {
  const [policy, setPolicy] = useState<EffectivePolicy | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const toast = useToast()

  useEffect(() => {
    api
      .getEffectivePolicy(apiKey.id)
      .then(setPolicy)
      .catch((e) => {
        const msg = e instanceof ApiError ? e.message : 'load failed'
        setErr(msg)
        toast.error('Could not load policy', msg)
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiKey.id])

  return (
    <Modal title={`Effective policy · ${apiKey.prefix}`} onClose={onClose}>
      {err ? (
        <div className="muted">{err}</div>
      ) : policy === null ? (
        <div className="muted">Loading…</div>
      ) : (
        <div className="flex flex-col gap-3">
          <div className="muted small">
            Period {new Date(policy.period_start).toLocaleDateString()} → {new Date(policy.period_end).toLocaleDateString()}
          </div>
          <PolicyLayerCard tone="team" layer={policy.team} />
          {policy.owner && (
            <PolicyLayerCard tone={policy.owner_kind === 'service_account' ? 'service_account' : 'user'} layer={policy.owner} />
          )}
          <PolicyLayerCard tone="key" layer={policy.key} />
          {policy.effective_allowed_models.length > 0 && (
            <div className="data-card" style={{ padding: 12 }}>
              <div className="data-h" style={{ fontSize: 13, marginBottom: 6 }}>Effective allowed models</div>
              <div className="flex flex-wrap gap-1">
                {policy.effective_allowed_models.map((m) => (
                  <Badge key={m} tone="neutral">{m}</Badge>
                ))}
              </div>
              <div className="muted small" style={{ marginTop: 6 }}>
                Intersection of team and key allowlists. <code className="mono">*</code> means no narrowing at that layer.
              </div>
            </div>
          )}
          {policy.notes && policy.notes.length > 0 && (
            <div className="data-card" style={{ padding: 12, borderColor: 'var(--warning)' }}>
              <div className="data-h" style={{ fontSize: 13, marginBottom: 6 }}>Notes</div>
              <ul className="muted small" style={{ paddingLeft: 18, margin: 0 }}>
                {policy.notes.map((n, i) => <li key={i}>{n}</li>)}
              </ul>
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}

function PolicyLayerCard({ tone, layer }: { tone: 'team' | 'user' | 'service_account' | 'key'; layer: PolicyLayer }) {
  const limit = layer.usd_limit_cents ?? 0
  const pct = limit > 0 ? Math.min(100, (layer.spend_so_far_cents / limit) * 100) : 0
  return (
    <div className="data-card" style={{ padding: 12 }}>
      <div className="flex items-center justify-between">
        <div className="data-h" style={{ fontSize: 13 }}>{layer.label}</div>
        <Badge tone="neutral" monospace={false}>{toneLabel(tone)}</Badge>
      </div>
      <dl className="kv-flat" style={{ marginTop: 8 }}>
        <dt>USD limit</dt>
        <dd>
          {layer.usd_limit_cents != null && layer.usd_limit_cents > 0 ? (
            <span className="mono">${(layer.spend_so_far_cents / 100).toFixed(2)} / ${(layer.usd_limit_cents / 100).toFixed(2)}</span>
          ) : (
            <span className="muted">no cap</span>
          )}
          {layer.period && <span className="muted" style={{ marginLeft: 6 }}>per {layer.period}</span>}
        </dd>
        {limit > 0 && (
          <>
            <dt></dt>
            <dd>
              <div style={{ height: 4, background: 'var(--surface-2, rgba(127,127,127,0.15))', borderRadius: 2, overflow: 'hidden' }}>
                <div style={{ width: `${pct}%`, height: '100%', background: pct > 90 ? 'var(--danger)' : pct > 75 ? 'var(--warning)' : 'var(--success)' }} />
              </div>
            </dd>
          </>
        )}
        <dt>RPM</dt>
        <dd className="mono">{layer.rpm != null ? layer.rpm : <span className="muted">unset</span>}</dd>
        <dt>TPM</dt>
        <dd className="mono">{layer.tpm != null ? layer.tpm : <span className="muted">unset</span>}</dd>
        <dt>Concurrent requests</dt>
        <dd className="mono">{layer.max_parallel_requests != null ? layer.max_parallel_requests : <span className="muted">unset</span>}</dd>
        {layer.allowed_models && layer.allowed_models.length > 0 && (
          <>
            <dt>Allowed models</dt>
            <dd className="mono small">{layer.allowed_models.join(', ')}</dd>
          </>
        )}
      </dl>
    </div>
  )
}

function toneLabel(tone: string): string {
  switch (tone) {
    case 'team': return 'team'
    case 'user': return 'user'
    case 'service_account': return 'service account'
    case 'key': return 'key'
    default: return tone
  }
}

function SkeletonRows({ cols }: { cols: number }) {
  return (
    <>
      {Array.from({ length: 5 }).map((_, i) => (
        <tr key={i} className="data-row">
          {Array.from({ length: cols }).map((__, j) => (
            <td key={j}><span className="skel" style={{ width: 80, display: 'block' }} /></td>
          ))}
        </tr>
      ))}
    </>
  )
}

const GRACE_OPTIONS = [
  { seconds: 0, label: 'Immediately', hint: 'The current key stops working now.' },
  { seconds: 3600, label: 'After 1 hour', hint: 'Both keys work for an hour, then the current one expires.' },
  { seconds: 86400, label: 'After 24 hours', hint: 'Both keys work for a day, then the current one expires.' },
  { seconds: 7 * 86400, label: 'After 7 days', hint: 'Both keys work for a week, then the current one expires.' },
]

// RotateKeyModal replaces a key with an identical one. A grace period keeps
// the old secret working while clients switch, so rotation needs no outage.
function RotateKeyModal({ apiKey, onClose, onRotated }: { apiKey: ApiKey; onClose: () => void; onRotated: (resp: CreateKeyResponse) => void }) {
  const [grace, setGrace] = useState(0)
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const option = GRACE_OPTIONS.find((o) => o.seconds === grace)!
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      onRotated(await api.rotateKey(apiKey.id, grace))
    } catch (err) {
      toast.error('Rotation failed', err instanceof ApiError ? err.message : undefined)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal title={`Rotate key ${apiKey.prefix}…`} onClose={onClose} size="md">
      <form onSubmit={submit} className="space-y-5">
        <p className="muted" style={{ margin: 0 }}>A new secret is issued with the same name, owner, models and limits.</p>
        <Field id="rotate-grace" label="Retire the current key" hint={option.hint}>
          <Select id="rotate-grace" value={grace} onChange={(e) => setGrace(Number(e.target.value))}>
            {GRACE_OPTIONS.map((o) => <option key={o.seconds} value={o.seconds}>{o.label}</option>)}
          </Select>
        </Field>
        <div className="modal-actions">
          <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" loading={busy}>Rotate</Button>
        </div>
      </form>
    </Modal>
  )
}
