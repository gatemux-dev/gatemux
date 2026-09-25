import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowRight, Boxes, CornerDownLeft, FileSearch, Plus, Search, UserCog, Users } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { api } from '../api/client'
import type { Principal } from '../auth'
import { principalIsAdmin } from '../auth'
import { navigationFor } from '../navigation'
import { useDebounced } from '../lib/useDebounced'

type Command = {
  id: string
  label: string
  hint?: string
  group: string
  icon: LucideIcon
  to: string
}

// CommandPalette (⌘K / Ctrl+K) jumps to any page, runs common create
// actions, and finds teams, aliases, users and requests by name or id.
// With hundreds of teams, typing a name beats paging a table.
export default function CommandPalette({ principal, open, onClose }: { principal: Principal; open: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const isAdmin = principalIsAdmin(principal)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [found, setFound] = useState<Command[]>([])
  const input = useRef<HTMLInputElement>(null)
  const q = useDebounced(query.trim(), 150)

  useEffect(() => {
    if (!open) return
    setQuery('')
    setActive(0)
    setFound([])
    window.setTimeout(() => input.current?.focus(), 0)
  }, [open])

  const staticCommands = useMemo<Command[]>(() => {
    const pages = navigationFor(principal).flatMap((g) =>
      g.items.filter((i) => !i.external).map((i) => ({
        id: `page:${i.to}`, label: i.label, group: 'Pages', icon: i.icon, to: i.to,
      })),
    )
    const actions: Command[] = isAdmin
      ? [
          { id: 'new:team', label: 'Create a team', group: 'Actions', icon: Plus, to: '/teams?new=1' },
          { id: 'new:alias', label: 'Add a model alias', group: 'Actions', icon: Plus, to: '/models?new=1' },
          { id: 'new:deployment', label: 'Add a deployment', group: 'Actions', icon: Plus, to: '/models?tab=deployments&new=1' },
          { id: 'new:invite', label: 'Invite a user', group: 'Actions', icon: Plus, to: '/invites?new=1' },
          { id: 'go:setup', label: 'Open the setup wizard', group: 'Actions', icon: ArrowRight, to: '/setup' },
        ]
      : []
    return [...pages, ...actions]
  }, [principal, isAdmin])

  // Records come from the server so the search covers every team, alias
  // and user, not whatever a page happened to load.
  useEffect(() => {
    if (!open || !isAdmin || q.length < 2) { setFound([]); return }
    let alive = true
    const results: Command[] = []
    // A bare number is most likely a request id, so it leads; otherwise the
    // matching records lead and "search logs" is the fallback at the end.
    if (/^\d+$/.test(q)) {
      results.push({ id: `req:${q}`, label: `Request #${q}`, hint: 'Open in logs', group: 'Requests', icon: FileSearch, to: `/usage?request=${q}` })
    }
    Promise.all([
      api.listTeams({ limit: 5 }, q).catch(() => null),
      api.listAliases({ limit: 5 }, { q }).catch(() => null),
      api.listUsers({ limit: 5 }, { q }).catch(() => null),
    ]).then(([teams, aliases, users]) => {
      if (!alive) return
      for (const t of teams?.items ?? []) results.push({ id: `team:${t.slug}`, label: t.name, hint: t.slug, group: 'Teams', icon: Users, to: `/teams/${encodeURIComponent(t.slug)}` })
      for (const a of aliases?.items ?? []) results.push({ id: `alias:${a.alias}`, label: a.alias, hint: `${(a.deployments ?? []).length} deployments`, group: 'Aliases', icon: Boxes, to: `/models/${encodeURIComponent(a.alias)}` })
      for (const u of users?.items ?? []) results.push({ id: `user:${u.id}`, label: u.name || u.email, hint: u.email, group: 'Users', icon: UserCog, to: `/users?q=${encodeURIComponent(u.email)}` })
      results.push({ id: `logs:${q}`, label: `Search logs for “${q}”`, group: 'Logs', icon: FileSearch, to: `/usage?q=${encodeURIComponent(q)}` })
      setFound(results)
    })
    return () => { alive = false }
  }, [q, open, isAdmin])

  const shown = useMemo(() => {
    const needle = query.trim().toLowerCase()
    const matches = needle ? staticCommands.filter((c) => c.label.toLowerCase().includes(needle)) : staticCommands
    return [...matches, ...found]
  }, [query, staticCommands, found])

  useEffect(() => { setActive(0) }, [query])

  if (!open) return null

  const run = (c: Command | undefined) => {
    if (!c) return
    onClose()
    navigate(c.to)
  }

  let lastGroup = ''
  return (
    <div className="cmdk-root" role="presentation" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="cmdk" role="dialog" aria-modal="true" aria-label="Command menu">
        <div className="cmdk-input">
          <Search size={16} />
          <input
            ref={input}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={isAdmin ? 'Search pages, teams, aliases, users or a request id…' : 'Search pages…'}
            aria-label="Search"
            aria-activedescendant={shown[active] ? `cmdk-${active}` : undefined}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setActive((i) => Math.min(i + 1, shown.length - 1)) }
              else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((i) => Math.max(i - 1, 0)) }
              else if (e.key === 'Enter') { e.preventDefault(); run(shown[active]) }
              else if (e.key === 'Escape') { e.preventDefault(); onClose() }
            }}
          />
          <kbd>Esc</kbd>
        </div>
        <ul className="cmdk-list" role="listbox">
          {shown.length === 0 && <li className="cmdk-empty">No matches.</li>}
          {shown.map((c, i) => {
            const header = c.group !== lastGroup ? c.group : null
            lastGroup = c.group
            const Icon = c.icon
            return (
              <li key={c.id} role="none">
                {header && <div className="cmdk-group">{header}</div>}
                <button
                  id={`cmdk-${i}`}
                  role="option"
                  aria-selected={i === active}
                  className={'cmdk-item' + (i === active ? ' is-active' : '')}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => run(c)}
                >
                  <Icon size={15} />
                  <span className="cmdk-label">{c.label}</span>
                  {c.hint && <span className="cmdk-hint">{c.hint}</span>}
                  {i === active && <CornerDownLeft size={13} className="cmdk-enter" />}
                </button>
              </li>
            )
          })}
        </ul>
      </div>
    </div>
  )
}
