import { ButtonHTMLAttributes, forwardRef, ReactNode } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from './cn'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success' | 'linkish'
type Size = 'sm' | 'md' | 'lg'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
  loading?: boolean
  leadingIcon?: ReactNode
  trailingIcon?: ReactNode
  fullWidth?: boolean
}

// Maps to handoff classes (.btn, .btn-primary, .btn-ghost, .linkish).
// Keep the API close to a typical button so call sites stay readable;
// the visual is governed by app.css, not Tailwind.
const variantClass: Record<Variant, string> = {
  primary:   'btn btn-primary',
  secondary: 'btn btn-secondary',
  ghost:     'btn btn-ghost',
  danger:    'btn btn-ghost text-[var(--danger)] border-[color:var(--danger)] hover:text-[var(--danger-fg)] hover:border-[color:var(--danger)]',
  success:   'btn btn-ghost text-[var(--success-fg)] border-[color:var(--success)] hover:bg-[var(--success-subtle)]',
  linkish:   'linkish',
}

// Sizes are classes (app.css) so call sites can still override via
// className; they only apply to .btn variants — linkish has no box.
const sizeClass: Record<Size, string> = {
  sm: 'btn-size-sm',
  md: '',
  lg: 'btn-size-lg',
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'primary',
    size = 'md',
    loading,
    leadingIcon,
    trailingIcon,
    fullWidth,
    className,
    children,
    disabled,
    type = 'button',
    style,
    ...rest
  },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      className={cn(
        variantClass[variant],
        variant !== 'linkish' && sizeClass[size],
        fullWidth && 'w-full justify-center',
        className,
      )}
      style={style}
      {...rest}
    >
      {loading ? (
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
      ) : (
        leadingIcon
      )}
      {children}
      {!loading && trailingIcon}
    </button>
  )
})
