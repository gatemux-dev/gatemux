import { useMemo, useState } from 'react'
import { Activity, AlertTriangle, CheckCircle2, RefreshCcw } from 'lucide-react'
import { api } from '../api/client'
import { useQuery } from '../lib/useQuery'
import {
  Button,
  EmptyRow,
  ErrorRow,
  MetricStrip,
  PageHeader,
  SegmentedFilter,
  SkeletonRows,
  StatTile,
  Table,
  Td,
  Tr,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

type StatusFilter = 'all' | '2xx' | '4xx' | '5xx'

export default function MyUsage() {
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)
  const [status, setStatus] = useState<StatusFilter>('all')

  const {
    data: page,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(() => api.listMyUsage({ limit, offset }), [limit, offset])
  const rows = page?.items ?? null
  const total = page?.total ?? 0

  const filtered = useMemo(() => {
    if (!rows) return null
    if (status === 'all') return rows
    if (status === '2xx') return rows.filter((r) => r.status_code < 300)
    if (status === '4xx') return rows.filter((r) => r.status_code >= 400 && r.status_code < 500)
    return rows.filter((r) => r.status_code >= 500)
  }, [rows, status])

  const counts = useMemo(() => {
    const c = { '2xx': 0, '4xx': 0, '5xx': 0 }
    rows?.forEach((r) => {
      if (r.status_code < 300) c['2xx']++
      else if (r.status_code < 500) c['4xx']++
      else c['5xx']++
    })
    return c
  }, [rows])

  const totals = useMemo(() => {
    if (!filtered) return null
    let prompt = 0,
      completion = 0,
      cost = 0,
      errors = 0
    for (const r of filtered) {
      prompt += r.prompt_tokens
      completion += r.completion_tokens
      cost += r.cost_cents
      if (r.status_code >= 400) errors++
    }
    return { count: filtered.length, prompt, completion, cost, errors }
  }, [filtered])

  return (
    <>
      <PageHeader
        title="My usage"
        description="Per-request log for keys assigned to you."
        actions={
          <Button variant="ghost" leadingIcon={<RefreshCcw size={13} />} onClick={reload}>
            Refresh
          </Button>
        }
      />

      <div className="filter-bar">
        <div className="filter-cluster">
          <SegmentedFilter
            value={status}
            onChange={setStatus}
            options={[
              { value: 'all', label: 'All', count: rows?.length || undefined },
              { value: '2xx', label: '2xx', count: counts['2xx'] || undefined, tone: 'ok' },
              { value: '4xx', label: '4xx', count: counts['4xx'] || undefined, tone: 'warn' },
              { value: '5xx', label: '5xx', count: counts['5xx'] || undefined, tone: 'err' },
            ]}
          />
        </div>
      </div>

      {totals && (
        <MetricStrip>
          <StatTile label="Requests" value={totals.count.toLocaleString()} />
          <StatTile label="Prompt tokens" value={totals.prompt.toLocaleString()} />
          <StatTile label="Completion tokens" value={totals.completion.toLocaleString()} />
          <StatTile label="Estimated cost" value={`$${(totals.cost / 100).toFixed(2)}`} tone="accent" />
          <StatTile
            label="Errors"
            value={totals.errors.toLocaleString()}
            tone={totals.errors > 0 ? 'warning' : 'neutral'}
          />
        </MetricStrip>
      )}

      <Table
        head={[
          'Time',
          'Team',
          'Alias',
          'Deployment',
          <span className="cell-num" key="p">Prompt</span>,
          <span className="cell-num" key="c">Completion</span>,
          <span className="cell-num" key="$">Cost</span>,
          <span className="cell-num" key="l">Latency</span>,
          'Status',
        ]}
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
          <SkeletonRows rows={6} cols={9} />
        ) : error && rows === null ? (
          <ErrorRow
            cols={9}
            title="Couldn't load usage"
            message={error.message}
            onRetry={reload}
            retrying={refreshing}
          />
        ) : (filtered ?? []).length === 0 ? (
          <EmptyRow cols={9}>
            <Activity size={18} />
            <span>No requests yet.</span>
          </EmptyRow>
        ) : (
          (filtered ?? []).map((r) => {
            const tone = r.status_code < 300 ? 'ok' : r.status_code < 500 ? 'warn' : 'err'
            return (
              <Tr key={r.id} error={r.status_code >= 400}>
                <Td className="muted mono tnum">{new Date(r.ts).toLocaleString()}</Td>
                <Td mono>{r.team_slug}</Td>
                <Td className="mono">{r.alias}</Td>
                <Td className="mono muted">{r.deployment_name}</Td>
                <Td num mono>{r.prompt_tokens.toLocaleString()}</Td>
                <Td num mono>{r.completion_tokens.toLocaleString()}</Td>
                <Td num mono>{r.cost_cents > 0 ? `$${(r.cost_cents / 100).toFixed(4)}` : '—'}</Td>
                <Td num mono>{r.latency_ms}ms</Td>
                <Td>
                  <span className={`pill pill-${tone} tnum`}>
                    {tone === 'ok' ? <CheckCircle2 size={11} /> : <AlertTriangle size={11} />}
                    {r.status_code}
                  </span>
                </Td>
              </Tr>
            )
          })
        )}
      </Table>
    </>
  )
}
