import { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { cn } from './cn'

interface CardProps {
  children: ReactNode
  className?: string
  padded?: boolean
}

// Card maps to the handoff's `.data-card` (a bordered surface). When
// `padded` is true we add inner padding; when false (default for tables)
// the card holds an edge-to-edge child with its own header/footer.
export function Card({ children, className, padded = true }: CardProps) {
  return (
    <div
      className={cn('data-card', padded && 'p-4', className)}
    >
      {children}
    </div>
  )
}

interface SectionProps {
  title?: ReactNode
  description?: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
}

// Section is the "data-card with a head bar" pattern. The handoff's
// `.data-head` carries title + count on the left, actions on the right.
export function Section({ title, description, action, children, className }: SectionProps) {
  return (
    <section className={cn('flex flex-col gap-3', className)}>
      {(title || action) && (
        <div className="flex items-end justify-between gap-3">
          <div>
            {title && <h2 className="data-h">{title}</h2>}
            {description && <div className="muted mt-1">{description}</div>}
          </div>
          {action && <div className="flex items-center gap-2">{action}</div>}
        </div>
      )}
      {children}
    </section>
  )
}

interface PageHeaderProps {
  /** Parent pages for detail views, e.g. [{ label: 'Models', to: '/models' }]. */
  crumbs?: { label: string; to: string }[]
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  metaRight?: ReactNode
}

// PageHeader is a title row: name on the left, the page's primary action
// on the right, one short line of context underneath when it earns its
// place. Detail pages pass crumbs so the way back is one click.
export function PageHeader({ crumbs, title, description, actions, metaRight }: PageHeaderProps) {
  return (
    <header className="flex flex-col gap-1">
      {crumbs && crumbs.length > 0 && (
        <nav className="crumbs" aria-label="Breadcrumb">
          {crumbs.map((c) => (
            <span key={c.to} className="flex items-center gap-1.5">
              <Link to={c.to}>{c.label}</Link>
              <span aria-hidden="true">/</span>
            </span>
          ))}
        </nav>
      )}
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <h1 className="page-title">{title}</h1>
          {description && <p className="page-sub">{description}</p>}
        </div>
        {(actions || metaRight) && (
          <div className="flex flex-wrap items-center gap-2">
            {metaRight}
            {actions}
          </div>
        )}
      </div>
    </header>
  )
}
