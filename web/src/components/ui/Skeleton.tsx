import { cn } from './cn'

interface SkeletonProps {
  className?: string
}

// Skeleton is a shimmer block (the .skel class carries the shimmer
// animation). Compose multiples with explicit widths to fake a row/card
// while data is loading.
export function Skeleton({ className }: SkeletonProps) {
  return <div className={cn('skel h-3 w-full', className)} />
}

// SkeletonRow is a tall row of skeleton cells. Used inside a Table tbody
// while the actual rows are loading so the layout doesn't jump.
export function SkeletonRow({ cols }: { cols: number }) {
  return (
    <tr>
      {Array.from({ length: cols }).map((_, i) => (
        <td key={i} className="px-4 py-3">
          <Skeleton className="w-3/4" />
        </td>
      ))}
    </tr>
  )
}

// SkeletonRows renders a block of placeholder rows for a table body —
// the shared replacement for the per-page hand-rolled variants.
export function SkeletonRows({ rows = 5, cols }: { rows?: number; cols: number }) {
  return (
    <>
      {Array.from({ length: rows }).map((_, i) => (
        <SkeletonRow key={i} cols={cols} />
      ))}
    </>
  )
}
