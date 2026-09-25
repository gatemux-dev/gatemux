import { describe, expect, it } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { useQuery } from './useQuery'

// A fetcher whose promises can be resolved out of order, to exercise the
// stale-response guard.
function deferredFetcher<T>() {
  const pending: Array<{ resolve: (v: T) => void; reject: (e: Error) => void }> = []
  const fetcher = () =>
    new Promise<T>((resolve, reject) => {
      pending.push({ resolve, reject })
    })
  return { fetcher, pending }
}

describe('useQuery', () => {
  it('moves loading → data on success', async () => {
    const { fetcher, pending } = deferredFetcher<string>()
    const { result } = renderHook(() => useQuery(fetcher, []))
    expect(result.current.loading).toBe(true)
    await act(async () => pending[0].resolve('hello'))
    await waitFor(() => expect(result.current.data).toBe('hello'))
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toBeNull()
  })

  it('surfaces the error when the first load fails', async () => {
    const { fetcher, pending } = deferredFetcher<string>()
    const { result } = renderHook(() => useQuery(fetcher, []))
    await act(async () => pending[0].reject(new Error('boom')))
    await waitFor(() => expect(result.current.error?.message).toBe('boom'))
    expect(result.current.data).toBeNull()
    expect(result.current.loading).toBe(false)
  })

  it('discards a stale response that resolves after a newer request', async () => {
    const { fetcher, pending } = deferredFetcher<string>()
    const { result, rerender } = renderHook(({ dep }) => useQuery(fetcher, [dep]), {
      initialProps: { dep: 1 },
    })
    rerender({ dep: 2 })
    await waitFor(() => expect(pending.length).toBe(2))
    // Newer request resolves first, then the older one limps in.
    await act(async () => pending[1].resolve('fresh'))
    await act(async () => pending[0].resolve('stale'))
    await waitFor(() => expect(result.current.data).toBe('fresh'))
    expect(result.current.data).not.toBe('stale')
  })

  it('keeps existing data visible during a reload and flags refreshing', async () => {
    const { fetcher, pending } = deferredFetcher<string>()
    const { result } = renderHook(() => useQuery(fetcher, []))
    await act(async () => pending[0].resolve('first'))
    await waitFor(() => expect(result.current.data).toBe('first'))
    act(() => result.current.reload())
    expect(result.current.data).toBe('first')
    expect(result.current.loading).toBe(false)
    expect(result.current.refreshing).toBe(true)
    await act(async () => pending[1].resolve('second'))
    await waitFor(() => expect(result.current.data).toBe('second'))
    expect(result.current.refreshing).toBe(false)
  })

  it('keeps stale data and reports the error when a refresh fails', async () => {
    const { fetcher, pending } = deferredFetcher<string>()
    const { result } = renderHook(() => useQuery(fetcher, []))
    await act(async () => pending[0].resolve('good'))
    await waitFor(() => expect(result.current.data).toBe('good'))
    act(() => result.current.reload())
    await act(async () => pending[1].reject(new Error('flaky')))
    await waitFor(() => expect(result.current.error?.message).toBe('flaky'))
    expect(result.current.data).toBe('good')
  })

  it('does nothing while disabled', async () => {
    const { fetcher, pending } = deferredFetcher<string>()
    const { result } = renderHook(() => useQuery(fetcher, [], { enabled: false }))
    expect(result.current.loading).toBe(false)
    expect(pending.length).toBe(0)
  })
})
