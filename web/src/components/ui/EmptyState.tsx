import { ReactNode } from 'react'
import { cn } from './cn'

interface EmptyStateProps {
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  className?: string
}

// EmptyState is the "no rows yet" surface. Used inside Card or Table.
// Designed to feel calm rather than alarming — empty is normal in a
// freshly-installed gateway.
export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 px-6 py-12 text-center',
        className,
      )}
    >
      {icon && (
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-bg-raised text-fg-muted">
          {icon}
        </div>
      )}
      <div className="space-y-1">
        <h3 className="text-sm font-medium text-fg-base">{title}</h3>
        {description && <p className="max-w-sm text-sm text-fg-muted">{description}</p>}
      </div>
      {action && <div className="pt-1">{action}</div>}
    </div>
  )
}
