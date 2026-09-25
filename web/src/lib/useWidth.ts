import { useCallback, useEffect, useState } from 'react'

// Tracks an element's content width so SVG charts can draw at true pixel
// size — a fixed viewBox scaled down to a card shrank axis text to ~8px.
// A callback ref (not useRef) because charts mount in an empty/loading
// state first; the measured node appears later and must still be observed.
export function useWidth<T extends HTMLElement>(fallback = 640) {
  const [node, setNode] = useState<T | null>(null)
  const [width, setWidth] = useState(fallback)
  const ref = useCallback((el: T | null) => setNode(el), [])
  useEffect(() => {
    if (!node) return
    const measure = () => setWidth(Math.max(200, Math.floor(node.getBoundingClientRect().width)))
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(node)
    return () => ro.disconnect()
  }, [node])
  return [ref, width] as const
}
