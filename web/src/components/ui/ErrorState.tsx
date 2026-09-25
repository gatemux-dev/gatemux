import { AlertTriangle } from 'lucide-react'
import { Button } from './Button'

interface ErrorStateProps {
  /** What failed, in the page's vocabulary — "Couldn't load teams". */
  title: string
  /** The underlying message, if one is worth showing. */
  message?: string
  onRetry?: () => void
  retrying?: boolean
}

// The shared failed-load panel: replaces the pattern where a fetch error
// only fired a toast and left the page on skeletons forever.
export function ErrorState({ title, message, onRetry, retrying }: ErrorStateProps) {
  return (
    <div role="alert" className="rows-empty flex flex-col items-center gap-2 py-10 text-center">
      <AlertTriangle aria-hidden className="h-5 w-5 text-warning-fg" />
      <div className="text-sm font-medium text-fg-base">{title}</div>
      {message && <div className="max-w-md text-xs text-fg-muted">{message}</div>}
      {onRetry && (
        <Button variant="secondary" size="sm" onClick={onRetry} loading={retrying} className="mt-1">
          Retry
        </Button>
      )}
    </div>
  )
}

// ErrorRow wraps ErrorState for use inside a table body.
export function ErrorRow({ cols, ...rest }: ErrorStateProps & { cols: number }) {
  return (
    <tr>
      <td colSpan={cols}>
        <ErrorState {...rest} />
      </td>
    </tr>
  )
}
