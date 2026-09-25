import { useState } from 'react'
import { CheckCircle2, KeyRound } from 'lucide-react'
import { api } from '../api/client'
import { useQuery } from '../lib/useQuery'
import type { MyKey } from '../types'
import {
  Badge,
  EmptyRow,
  ErrorRow,
  PageHeader,
  SkeletonRows,
  Table,
  Td,
  Tr,
} from '../components/ui'
import { Pagination } from '../components/Pagination'

export default function MyKeys() {
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)

  const {
    data: page,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(() => api.listMyKeys({ limit, offset }), [limit, offset])
  const keys = page?.items ?? null
  const total = page?.total ?? 0

  return (
    <>
      <PageHeader
        title="My keys"
        description="Virtual keys assigned to you."
      />

      <Table
        head={['Prefix', 'Label', 'Allowed models', 'Limits', 'Expires', 'Last used', 'Status']}
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
          <SkeletonRows rows={5} cols={7} />
        ) : error && keys === null ? (
          <ErrorRow
            cols={7}
            title="Couldn't load keys"
            message={error.message}
            onRetry={reload}
            retrying={refreshing}
          />
        ) : (keys ?? []).length === 0 ? (
          <EmptyRow cols={7}>
            <KeyRound size={18} />
            <span>No keys assigned to you yet.</span>
          </EmptyRow>
        ) : (
          (keys ?? []).map((k) => (
            <Tr key={k.id}>
              <Td mono>{k.prefix}…</Td>
              <Td>{k.name || <span className="muted">—</span>}</Td>
              <Td>{renderAllowed(k.allowed_models)}</Td>
              <Td>{renderLimits(k)}</Td>
              <Td className="muted">{k.expires_at ? new Date(k.expires_at).toLocaleString() : 'never'}</Td>
              <Td className="muted">{k.last_used_at ? new Date(k.last_used_at).toLocaleString() : '—'}</Td>
              <Td>{renderStatus(k)}</Td>
            </Tr>
          ))
        )}
      </Table>
    </>
  )
}

function renderAllowed(allowed: string[]) {
  if (!allowed || allowed.length === 0 || (allowed.length === 1 && allowed[0] === '*')) {
    return <span className="muted">all team models</span>
  }
  return <span className="mono muted">{allowed.join(', ')}</span>
}

function renderLimits(k: MyKey) {
  const parts: string[] = []
  if (k.rpm) parts.push(`${k.rpm} rpm`)
  if (k.tpm) parts.push(`${k.tpm.toLocaleString()} tpm`)
  if (parts.length === 0) return <span className="muted">team default</span>
  return <span className="muted">{parts.join(' · ')}</span>
}

function renderStatus(k: MyKey) {
  if (k.revoked_at) return <Badge tone="neutral" monospace={false}>revoked</Badge>
  if (k.paused_at) return <Badge tone="warning" monospace={false}>paused</Badge>
  if (k.expires_at && new Date(k.expires_at) < new Date()) {
    return <Badge tone="warning" monospace={false}>expired</Badge>
  }
  return (
    <span className="pill pill-ok"><CheckCircle2 size={11} /> active</span>
  )
}

