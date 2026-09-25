import { useEffect, useState } from 'react'
import { Lock } from 'lucide-react'
import { api, ApiError } from '../api/client'
import type { CreateInviteResponse, Team } from '../types'
import { principalRole, type Principal } from '../auth'
import { Button, Field, Input, Modal, ModalFooter, Select } from './ui'

type Role = 'admin' | 'manager' | 'member'

export default function CreateInviteModal({
  principal,
  team,
  onClose,
  onCreated,
}: {
  principal: Principal
  /** Opened from a team's page: the invite joins that team. */
  team?: string
  onClose: () => void
  onCreated: (resp: CreateInviteResponse) => void
}) {
  const isAdmin = principalRole(principal) === 'admin'
  const ownTeam = principal.user?.team_slug

  // Manager mode is locked to (member, own_team). The backend enforces
  // the same rules; this just surfaces the constraint clearly so the user
  // doesn't try (and fail) to invite an admin or pick another team.
  const [email, setEmail] = useState('')
  const [teamSlug, setTeamSlug] = useState(team ?? (isAdmin ? '' : (ownTeam ?? '')))
  const [role, setRole] = useState<Role>(isAdmin ? 'member' : 'member')
  const [hours, setHours] = useState('168')
  const [teams, setTeams] = useState<Team[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    if (!isAdmin) return
    api
      .listTeams({ limit: 200 })
      .then((p) => setTeams(p.items))
      .catch(() => {})
  }, [isAdmin])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    try {
      const r = await api.createInvite({
        email: email.trim() || undefined,
        team_slug: teamSlug || undefined,
        role,
        expires_in_hours: hours ? parseInt(hours, 10) : undefined,
      })
      onCreated(r)
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'failed to create invite')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Create invite" onClose={onClose} dismissible={false}>
      {!isAdmin && (
        <div className="mb-4 flex items-start gap-2 rounded-md border border-border-base bg-bg-base/50 p-3 text-xs text-fg-base">
          <Lock className="mt-0.5 h-3.5 w-3.5 text-fg-muted" />
          <div>
            Managers can only invite members into their own team
            {ownTeam ? (
              <>
                {' '}(<code className="rounded bg-bg-raised px-1 py-0.5 font-mono text-[11px]">{ownTeam}</code>)
              </>
            ) : null}
            . Ask an admin if you need to invite another role or onboard
            into a different team.
          </div>
        </div>
      )}

      <form onSubmit={submit} className="space-y-4">
        <Field
          label="Email (optional)"
          hint="If set, only this email can accept the invite."
        >
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="alice@example.com"
          />
        </Field>

        {isAdmin ? (
          <>
            <Field label="Team (optional)">
              <Select
                value={teamSlug}
                onChange={(e) => setTeamSlug(e.target.value)}
              >
                <option value="">— individual (no team) —</option>
                {teams.map((t) => (
                  <option key={t.slug} value={t.slug}>
                    {t.slug} · {t.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field
              label="Role"
              hint="Managers control one team's keys + members. Admins can do everything globally."
            >
              <Select
                value={role}
                onChange={(e) => setRole(e.target.value as Role)}
              >
                <option value="member">member</option>
                <option value="manager">manager</option>
                <option value="admin">admin</option>
              </Select>
            </Field>
          </>
        ) : (
          <div className="grid gap-2 rounded-md border border-border-base bg-bg-surface p-3 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-fg-subtle">Role</span>
              <span className="rounded bg-bg-raised px-2 py-0.5 font-mono text-[11px] text-fg-base">
                member
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-fg-subtle">Team</span>
              <span className="font-mono text-[11px] text-fg-base">
                {ownTeam ?? '—'}
              </span>
            </div>
          </div>
        )}

        <Field
          label="Expires in (hours)"
          hint="Default: 168 (7 days). Max: 720 (30 days)."
        >
          <Input
            type="number"
            min="1"
            max="720"
            value={hours}
            onChange={(e) => setHours(e.target.value)}
            placeholder="168"
          />
        </Field>

        {err && <p className="text-xs text-danger">{err}</p>}

        <ModalFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={!isAdmin && !ownTeam}>
            {busy ? 'Generating…' : 'Generate invite'}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}
