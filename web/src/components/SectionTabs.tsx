import { Link, useSearchParams } from 'react-router-dom'

export function useSectionTab<T extends string>(allowed: readonly T[], fallback: T) {
  const [params, setParams] = useSearchParams()
  const candidate = params.get('tab') as T
  const tab = allowed.includes(candidate) ? candidate : fallback
  const setTab = (next: T) => setParams((previous) => {
    const result = new URLSearchParams(previous)
    result.set('tab', next)
    return result
  })
  return [tab, setTab] as const
}

// Real links make sections bookmarkable; arrow keys follow the tab pattern.
export default function SectionTabs({ label, current, items }: {
  label: string; current: string; items: { id: string; label: string; count?: number }[]
}) {
  const [params] = useSearchParams()
  return <div className="section-tabs" role="tablist" aria-label={label}>
    {items.map((item) => {
      const query = new URLSearchParams(params)
      query.set('tab', item.id)
      return <Link key={item.id} to={`?${query}`} role="tab" aria-selected={current === item.id}
        tabIndex={current === item.id ? 0 : -1} className={'section-tab' + (current === item.id ? ' is-on' : '')}
        onKeyDown={(event) => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
          event.preventDefault()
          const links = Array.from(event.currentTarget.parentElement!.querySelectorAll<HTMLAnchorElement>('[role="tab"]'))
          const index = links.indexOf(event.currentTarget)
          const next = event.key === 'Home' ? 0 : event.key === 'End' ? links.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + links.length) % links.length
          links[next].focus()
          links[next].click()
        }}>
        {item.label}{item.count != null && <span className="tab-count tnum">{item.count}</span>}
      </Link>
    })}
  </div>
}
