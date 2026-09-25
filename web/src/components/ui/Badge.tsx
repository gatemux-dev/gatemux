import { ReactNode } from 'react'
import { cn } from './cn'

type Tone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger'

interface BadgeProps {
  tone?: Tone
  children: ReactNode
  className?: string
  monospace?: boolean
}

// Maps to the `.pill` family. Status tones (success/warning/danger) are
// mono data chips; neutral label badges render in the sans face. Pass
// monospace={false} to force sans on a status tone.
const tones: Record<Tone, string> = {
  // Neutral and accent badges label things (roles, capabilities); they
  // render as tags. Only success/warning/danger carry a status dot.
  neutral: 'pill pill-neutral pill-tag',
  accent: 'pill pill-accent pill-tag',
  success: 'pill pill-ok',
  warning: 'pill pill-warn',
  danger: 'pill pill-err',
}

export function Badge({ tone = 'neutral', children, className, monospace }: BadgeProps) {
  return (
    <span
      className={cn(
        tones[tone],
        monospace === false && '!font-sans',
        className,
      )}
    >
      {children}
    </span>
  )
}
