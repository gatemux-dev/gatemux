import { useEffect, useState } from 'react'

// Returns value after it has stopped changing for `ms`, so search boxes
// query the server once per pause instead of once per keystroke.
export function useDebounced<T>(value: T, ms = 300): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(value), ms)
    return () => window.clearTimeout(t)
  }, [value, ms])
  return debounced
}
