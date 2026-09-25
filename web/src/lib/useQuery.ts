import { useCallback, useEffect, useRef, useState } from 'react'

export interface QueryState<T> {
  /** Last successful result. Stays visible during refetches. */
  data: T | null
  /** Last failure. Cleared by the next successful fetch. */
  error: Error | null
  /** True only while loading with no data yet — render skeletons on this. */
  loading: boolean
  /** True while a refetch runs behind existing data. */
  refreshing: boolean
  /** Re-run the fetch (after a mutation, or from a Retry button). */
  reload: () => void
}

interface QueryOptions {
  /** Refetch on this interval. Ticks are skipped while the tab is hidden
   *  or while a previous request is still in flight. */
  pollMs?: number
  /** When false the hook idles (no fetch, loading stays false). */
  enabled?: boolean
}

// useQuery is the shared data-fetch hook: it owns the loading/error state
// machine every page used to hand-roll, and — via a version counter —
// guarantees a slow response can never overwrite a newer one (the
// stale-response race the old per-page effects had).
export function useQuery<T>(
  fetcher: () => Promise<T>,
  deps: unknown[],
  opts: QueryOptions = {},
): QueryState<T> {
  const { pollMs, enabled = true } = opts
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<Error | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [refreshing, setRefreshing] = useState(false)

  const versionRef = useRef(0)
  const inFlightRef = useRef(false)
  const hasDataRef = useRef(false)
  // The fetcher is intentionally not a dependency: pages pass inline
  // closures, and re-running on identity change would refetch every render.
  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher

  const run = useCallback(
    (isPollTick: boolean) => {
      if (!enabled) return
      if (isPollTick && inFlightRef.current) return
      const version = ++versionRef.current
      inFlightRef.current = true
      if (hasDataRef.current) setRefreshing(true)
      else {
        setLoading(true)
        setError(null)
      }
      fetcherRef.current().then(
        (result) => {
          if (version !== versionRef.current) return
          hasDataRef.current = true
          setData(result)
          setError(null)
          setLoading(false)
          setRefreshing(false)
          inFlightRef.current = false
        },
        (err) => {
          if (version !== versionRef.current) return
          setError(err instanceof Error ? err : new Error(String(err)))
          setLoading(false)
          setRefreshing(false)
          inFlightRef.current = false
        },
      )
    },
    [enabled],
  )

  // deps changing means "this is a different query now" — fetch again.
  // The array is spread so callers pass deps exactly like useEffect's.
  useEffect(() => {
    run(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, run])

  useEffect(() => {
    if (!pollMs || !enabled) return
    const id = window.setInterval(() => {
      if (document.visibilityState === 'hidden') return
      run(true)
    }, pollMs)
    return () => window.clearInterval(id)
  }, [pollMs, enabled, run])

  const reload = useCallback(() => run(false), [run])

  return { data, error, loading, refreshing, reload }
}
