import { ChevronLeft, ChevronRight } from 'lucide-react'

interface PaginationProps {
  total: number
  limit: number
  offset: number
  onChange: (next: { limit: number; offset: number }) => void
  pageSizeOptions?: number[]
}

// Pagination uses the handoff's `.data-foot` + `.btn .btn-ghost` shape so
// it reads like a natural footer underneath any `.data-table`.
export function Pagination({
  total,
  limit,
  offset,
  onChange,
  pageSizeOptions = [25, 50, 100, 200],
}: PaginationProps) {
  // An empty table already says so in its empty state; a pager with
  // "No results · Prev · Next" only adds noise.
  if (total === 0 && offset === 0) return null
  const safeLimit = Math.max(1, limit)
  const start = total === 0 ? 0 : Math.min(offset + 1, total)
  const end = Math.min(offset + safeLimit, total)
  const onLastPage = end >= total
  const onFirstPage = offset === 0

  return (
    <div className="data-foot">
      <div className="muted tnum">
        {total === 0 ? (
          <span>No results</span>
        ) : (
          <span>
            <strong className="text-fg-base">{start.toLocaleString()}</strong>–
            <strong className="text-fg-base">{end.toLocaleString()}</strong> of{' '}
            <strong className="text-fg-base">{total.toLocaleString()}</strong>
          </span>
        )}
      </div>
      <div className="flex items-center gap-2">
        <select
          aria-label="Results per page"
          value={safeLimit}
          onChange={(e) => onChange({ limit: Number(e.target.value), offset: 0 })}
          className="select-shell text-[12.5px]"
          style={{ paddingRight: 8 }}
        >
          {pageSizeOptions.map((opt) => (
            <option key={opt} value={opt}>
              {opt} / page
            </option>
          ))}
        </select>
        <div className="pager">
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => onChange({ limit: safeLimit, offset: Math.max(0, offset - safeLimit) })}
            disabled={onFirstPage}
          >
            <ChevronLeft size={14} />
            Prev
          </button>
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => onChange({ limit: safeLimit, offset: offset + safeLimit })}
            disabled={onLastPage}
          >
            Next
            <ChevronRight size={14} />
          </button>
        </div>
      </div>
    </div>
  )
}
