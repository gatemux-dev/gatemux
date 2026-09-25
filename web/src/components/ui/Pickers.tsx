import { ReactNode, useEffect, useId, useRef, useState } from 'react'
import { Check, ChevronDown, GripVertical, X } from 'lucide-react'

// MultiSelect picks several values from a long list: selected values show
// as removable chips, typing filters the list, and Enter adds the top
// match. When `ordered`, chips can be reordered (priority lists).
export function MultiSelect({
  id,
  options,
  value,
  onChange,
  placeholder = 'Search…',
  ordered = false,
  describe,
  emptyText = 'No matches',
  mono = true,
}: {
  id?: string
  options: string[]
  value: string[]
  onChange: (next: string[]) => void
  placeholder?: string
  ordered?: boolean
  describe?: (option: string) => ReactNode
  emptyText?: string
  mono?: boolean
}) {
  const autoId = useId()
  const inputId = id ?? autoId
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const wrap = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  const needle = query.trim().toLowerCase()
  const matches = options.filter((o) => !value.includes(o) && (!needle || o.toLowerCase().includes(needle))).slice(0, 50)
  useEffect(() => { setActive(0) }, [query])

  const add = (o: string) => { onChange([...value, o]); setQuery('') }
  const remove = (o: string) => onChange(value.filter((v) => v !== o))
  const move = (index: number, delta: number) => {
    const next = [...value]
    const target = index + delta
    if (target < 0 || target >= next.length) return
    ;[next[index], next[target]] = [next[target], next[index]]
    onChange(next)
  }

  return (
    <div className="picker" ref={wrap}>
      {value.length > 0 && (
        <ol className={'picker-chips' + (ordered ? ' is-ordered' : '')}>
          {value.map((v, i) => (
            <li key={v} className="picker-chip">
              {ordered && <GripVertical size={12} className="picker-grip" aria-hidden="true" />}
              {ordered && <span className="picker-rank">{i + 1}</span>}
              <span className={mono ? 'mono' : undefined}>{v}</span>
              {ordered && (
                <>
                  <button type="button" onClick={() => move(i, -1)} disabled={i === 0} aria-label={`Move ${v} up`}>↑</button>
                  <button type="button" onClick={() => move(i, 1)} disabled={i === value.length - 1} aria-label={`Move ${v} down`}>↓</button>
                </>
              )}
              <button type="button" onClick={() => remove(v)} aria-label={`Remove ${v}`}><X size={12} /></button>
            </li>
          ))}
        </ol>
      )}
      <div className="picker-input">
        <input
          id={inputId}
          value={query}
          placeholder={placeholder}
          role="combobox"
          aria-expanded={open}
          aria-autocomplete="list"
          onFocus={() => setOpen(true)}
          onChange={(e) => { setQuery(e.target.value); setOpen(true) }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') { e.preventDefault(); setOpen(true); setActive((a) => Math.min(a + 1, matches.length - 1)) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(a - 1, 0)) }
            else if (e.key === 'Enter') { e.preventDefault(); if (matches[active]) add(matches[active]) }
            else if (e.key === 'Escape') { setOpen(false) }
            else if (e.key === 'Backspace' && !query && value.length > 0) remove(value[value.length - 1])
          }}
        />
        <ChevronDown size={14} aria-hidden="true" />
      </div>
      {open && (
        <ul className="picker-list" role="listbox">
          {matches.length === 0 ? (
            <li className="picker-empty">{emptyText}</li>
          ) : matches.map((o, i) => (
            <li key={o} role="option" aria-selected={i === active}>
              <button
                type="button"
                className={'picker-option' + (i === active ? ' is-active' : '')}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => add(o)}
              >
                <span className={mono ? 'mono' : undefined}>{o}</span>
                {describe && <span className="picker-desc">{describe(o)}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// SearchSelect picks one record from a server-side search (for example a
// team member among thousands). `null` means the explicit empty choice.
export function SearchSelect<T>({
  id,
  value,
  onChange,
  search,
  label,
  detail,
  emptyChoice,
  placeholder = 'Search…',
}: {
  id?: string
  value: T | null
  onChange: (next: T | null) => void
  search: (query: string) => Promise<T[]>
  label: (item: T) => string
  detail?: (item: T) => string
  emptyChoice: string
  placeholder?: string
}) {
  const autoId = useId()
  const inputId = id ?? autoId
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [items, setItems] = useState<T[]>([])
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState(false)
  const wrap = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => { if (!wrap.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  useEffect(() => {
    if (!open) return
    let alive = true
    setLoading(true)
    const t = window.setTimeout(() => {
      search(query.trim())
        .then((r) => { if (alive) { setItems(r); setFailed(false) } })
        .catch(() => { if (alive) setFailed(true) })
        .finally(() => { if (alive) setLoading(false) })
    }, 200)
    return () => { alive = false; window.clearTimeout(t) }
    // search is stable per dialog; re-running on identity would refetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, open])

  const pick = (item: T | null) => { onChange(item); setOpen(false); setQuery('') }

  return (
    <div className="picker" ref={wrap}>
      <button
        id={inputId}
        type="button"
        className="picker-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={value ? undefined : 'muted'}>{value ? label(value) : emptyChoice}</span>
        {value && detail && <span className="picker-desc">{detail(value)}</span>}
        <ChevronDown size={14} aria-hidden="true" />
      </button>
      {open && (
        <div className="picker-list picker-panel">
          <input autoFocus className="picker-search" value={query} placeholder={placeholder} onChange={(e) => setQuery(e.target.value)} aria-label="Search" />
          <ul role="listbox">
            <li role="option" aria-selected={value === null}>
              <button type="button" className="picker-option" onClick={() => pick(null)}>
                <span>{emptyChoice}</span>{value === null && <Check size={13} />}
              </button>
            </li>
            {failed && <li className="picker-empty">Search is unavailable. Try again.</li>}
            {!failed && loading && items.length === 0 && <li className="picker-empty">Searching…</li>}
            {!failed && !loading && items.length === 0 && <li className="picker-empty">No matches</li>}
            {items.map((item, i) => (
              <li key={i} role="option" aria-selected={false}>
                <button type="button" className="picker-option" onClick={() => pick(item)}>
                  <span>{label(item)}</span>
                  {detail && <span className="picker-desc">{detail(item)}</span>}
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

// Disclosure tucks optional settings behind one line, so a dialog leads
// with what most people need and still offers everything.
export function Disclosure({ title, summary, defaultOpen = false, children }: {
  title: string
  summary?: ReactNode
  defaultOpen?: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className={'disclosure' + (open ? ' is-open' : '')}>
      <button type="button" className="disclosure-head" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <ChevronDown size={14} className="disclosure-chevron" aria-hidden="true" />
        <span className="disclosure-title">{title}</span>
        {!open && summary && <span className="disclosure-summary">{summary}</span>}
      </button>
      {open && <div className="disclosure-body">{children}</div>}
    </div>
  )
}
