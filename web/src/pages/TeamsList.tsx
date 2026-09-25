import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ChevronRight, Mail, Plus, Search } from 'lucide-react'
import { api } from '../api/client'
import { useQuery } from '../lib/useQuery'
import { useOpenFromUrl } from '../lib/useOpenFromUrl'
import { fmtUSD } from '../lib/money'
import type { Team } from '../types'
import CreateTeamModal from '../components/CreateTeamModal'
import {
  Button,
  EmptyRow,
  ErrorRow,
  PageHeader,
  SkeletonRows,
  Table,
  Td,
  Tr,
  useToast,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

export default function TeamsList() {
  const navigate = useNavigate()
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)
  const [creating, setCreating] = useState(false)
  useOpenFromUrl(() => setCreating(true))
  const [q, setQ] = useState('')
  // The search runs server-side (debounced) so it covers every team, not
  // just the 50 loaded on the current page.
  const [debounced, setDebounced] = useState('')
  const toast = useToast()

  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(q.trim()), 300)
    return () => window.clearTimeout(t)
  }, [q])
  useEffect(() => {
    setOffset(0)
  }, [debounced])

  const {
    data: page,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(() => api.listTeams({ limit, offset }, debounced, { stats: true }), [limit, offset, debounced])
  const teams = page?.items ?? null
  const total = page?.total ?? 0
  const filtered = teams ?? []

  return (
    <>
      <PageHeader
        title="Teams"
        description="Members, keys, budgets, and limits per team."
        actions={
          <>
            <Link to="/invites">
              <Button variant="ghost" leadingIcon={<Mail size={13} />}>Invites</Button>
            </Link>
            <Button leadingIcon={<Plus size={13} />} onClick={() => setCreating(true)}>New team</Button>
          </>
        }
      />

      <div className="page-toolbar">
        <div className="page-toolbar-l">
          <label className="search-shell">
            <Search size={13} />
            <input aria-label="Search all teams" placeholder="Search teams…" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
        </div>
      </div>

      <Table
        head={['Team', 'Spend this period', <span className="cell-num" key="k">Active keys</span>, <span className="cell-num" key="m">Members</span>, <span className="cell-num" key="rpm">RPM</span>, '']}
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
          <SkeletonRows rows={6} cols={6} />
        ) : error && teams === null ? (
          <ErrorRow
            cols={6}
            title="Couldn't load teams"
            message={error.message}
            onRetry={reload}
            retrying={refreshing}
          />
        ) : filtered.length === 0 ? (
          <EmptyRow cols={6}>No teams match.</EmptyRow>
        ) : (
          filtered.map((t) => (
            <Tr key={t.id} onClick={() => navigate(`/teams/${encodeURIComponent(t.slug)}`)}>
              <Td>
                <Link to={`/teams/${encodeURIComponent(t.slug)}`} onClick={(event) => event.stopPropagation()} style={{ textDecoration: 'none', color: 'inherit' }}>
                  <div style={{ fontWeight: 560 }}>{t.name}</div>
                  <div className="mono muted" style={{ marginTop: 2 }}>{t.slug}</div>
                </Link>
              </Td>
              <Td><TeamSpend team={t} /></Td>
              <Td num className="tnum">{t.stats ? t.stats.active_keys.toLocaleString() : '—'}</Td>
              <Td num className="tnum">{t.stats ? t.stats.members.toLocaleString() : '—'}</Td>
              <Td num>
                {t.rpm == null ? <span className="muted">—</span> : <span className="tnum">{t.rpm.toLocaleString()}</span>}
              </Td>
              <Td align="right">
                <Link className="linkish" aria-label={`Open ${t.name}`} to={`/teams/${encodeURIComponent(t.slug)}`} onClick={(event) => event.stopPropagation()}>
                  Open <ChevronRight size={11} />
                </Link>
              </Td>
            </Tr>
          ))
        )}
      </Table>

      {creating && (
        <CreateTeamModal
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            toast.success('Team created')
            reload()
          }}
        />
      )}
    </>
  )
}

// TeamSpend shows spend in the current budget period against the budget,
// turning amber at 80% and red at the limit.
function TeamSpend({ team }: { team: Team }) {
  const spent = team.stats?.period_spend_cents
  if (spent == null) return <span className="muted">—</span>
  const limit = team.usd_limit_cents
  if (limit == null || limit <= 0) {
    return <span className="tnum">{fmtUSD(spent)} <span className="muted">no budget</span></span>
  }
  const ratio = spent / limit
  return (
    <span className="share" title={`${fmtUSD(spent)} of ${fmtUSD(limit)} this ${team.period}`}>
      <span className={'share-bar' + (ratio >= 1 ? ' is-over' : ratio >= 0.8 ? ' is-warn' : '')}>
        <span style={{ width: `${Math.min(ratio * 100, 100)}%` }} />
      </span>
      <span className="tnum">{fmtUSD(spent)} <span className="muted">/ {fmtUSD(limit)}</span></span>
    </span>
  )
}
