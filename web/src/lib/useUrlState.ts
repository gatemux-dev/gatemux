import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

// Filters live in the URL so the back button, reloads, bookmarks and shared
// links all reproduce the same view. Defaults are omitted from the URL.
export function useUrlState<T extends Record<string, string>>(defaults: T) {
  const [params, setParams] = useSearchParams()
  const values = Object.fromEntries(
    Object.entries(defaults).map(([k, d]) => [k, params.get(k) ?? d]),
  ) as T
  const update = useCallback(
    (patch: Partial<T>, opts: { push?: boolean } = {}) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(patch)) {
            if (v === undefined || v === '' || v === defaults[k]) next.delete(k)
            else next.set(k, v as string)
          }
          return next
        },
        { replace: !opts.push },
      )
    },
    // defaults is a literal at each call site; its identity doesn't matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [setParams],
  )
  return [values, update] as const
}
