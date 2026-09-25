import { useState } from 'react'
import { Plus } from 'lucide-react'
import { api } from '../api/client'
import { fmtDateTime } from '../lib/format'
import { useQuery } from '../lib/useQuery'
import { useOpenFromUrl } from '../lib/useOpenFromUrl'
import type { CreateInviteResponse } from '../types'
import type { Principal } from '../auth'
import CreateInviteModal from '../components/CreateInviteModal'
import RevealInviteModal from '../components/RevealInviteModal'
import {
  Badge,
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

export default function Invites({ principal, embedded = false }: { principal: Principal; embedded?: boolean }) {
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)
  const [creating, setCreating] = useState(false)
  useOpenFromUrl(() => setCreating(true))
  const [reveal, setReveal] = useState<CreateInviteResponse | null>(null)
  const toast = useToast()

  const {
    data: page,
    error,
    loading,
    refreshing,
    reload,
  } = useQuery(() => api.listInvites({ limit, offset }), [limit, offset])
  const invites = page?.items ?? null
  const total = page?.total ?? 0

  return (
    <>
      {embedded ? (
        <div className="flex items-center justify-between gap-3">
          <p className="muted" style={{ margin: 0, fontSize: 13.5 }}>Single-use links bound to one email. Each link is shown once.</p>
          <Button leadingIcon={<Plus size={13} />} onClick={() => setCreating(true)}>Send invite</Button>
        </div>
      ) : (
        <PageHeader
          title="Invitations"
          description="Single-use links bound to one email. Each link is shown once."
          actions={<Button leadingIcon={<Plus size={13} />} onClick={() => setCreating(true)}>Send invite</Button>}
        />
      )}

      <Table
        head={['Prefix', 'Email', 'Team', 'Role', 'Expires', 'Status']}
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
        ) : error && invites === null ? (
          <ErrorRow
            cols={6}
            title="Couldn't load invites"
            message={error.message}
            onRetry={reload}
            retrying={refreshing}
          />
        ) : (invites ?? []).length === 0 ? (
          <EmptyRow cols={6}>No invites yet.</EmptyRow>
        ) : (
          (invites ?? []).map((inv) => {
            const expired = new Date(inv.expires_at) < new Date()
            const tone = inv.accepted_at ? 'success' : expired ? 'neutral' : 'accent'
            return (
              <Tr key={inv.id}>
                <Td mono>{inv.prefix}…</Td>
                <Td className="mono">{inv.email || <span className="muted">any</span>}</Td>
                <Td>
                  {inv.team_slug ? (
                    <span className="mono muted">{inv.team_slug}</span>
                  ) : (
                    <span className="muted">individual</span>
                  )}
                </Td>
                <Td>
                  <Badge tone={inv.role === 'admin' ? 'accent' : 'neutral'} monospace={false}>{inv.role}</Badge>
                </Td>
                <Td className="muted tnum">{fmtDateTime(inv.expires_at)}</Td>
                <Td>
                  <Badge tone={tone} monospace={false}>
                    {inv.accepted_at ? 'accepted' : expired ? 'expired' : 'pending'}
                  </Badge>
                </Td>
              </Tr>
            )
          })
        )}
      </Table>

      {creating &&
        (() => {
          return (
            <CreateInviteModal
              principal={principal}
              onClose={() => setCreating(false)}
              onCreated={(resp) => {
                setCreating(false)
                setReveal(resp)
                toast.success('Invite created')
                reload()
              }}
            />
          )
        })()}
      {reveal && <RevealInviteModal resp={reveal} onClose={() => setReveal(null)} />}
    </>
  )
}

