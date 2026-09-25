import { useEffect } from 'react'
import { useSearchParams } from 'react-router-dom'

// Opens a create dialog when the page is reached with ?new=1 (from the
// command menu or a shared link), then drops the flag so a reload or the
// back button doesn't reopen it.
export function useOpenFromUrl(open: () => void) {
  const [params, setParams] = useSearchParams()
  useEffect(() => {
    if (params.get('new') !== '1') return
    open()
    setParams((prev) => {
      const next = new URLSearchParams(prev)
      next.delete('new')
      return next
    }, { replace: true })
    // Runs once per arrival with the flag; open is a stable setter wrapper.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params.get('new')])
}
