import {
  Children,
  cloneElement,
  forwardRef,
  isValidElement,
  useId,
  InputHTMLAttributes,
  ReactNode,
  TextareaHTMLAttributes,
  SelectHTMLAttributes,
} from 'react'
import { cn } from './cn'

interface FieldProps {
  label?: ReactNode
  hint?: ReactNode
  error?: string | null
  required?: boolean
  id?: string
  children: ReactNode
}

export function Field({ label, hint, error, required, id, children }: FieldProps) {
  // Most call sites never passed an id, leaving the label unassociated —
  // screen readers announced the control as unlabelled and clicking the
  // label did nothing. Generate one and clone it onto the first element
  // child (Input/Textarea/Select forward it to the real control).
  const autoId = useId()
  const fieldId = id ?? autoId
  let injected = false
  const kids = Children.map(children, (child) => {
    if (!injected && isValidElement(child)) {
      injected = true
      const childProps = child.props as { id?: string }
      if (!childProps.id) return cloneElement(child, { id: fieldId } as Partial<unknown>)
    }
    return child
  })
  return (
    <div className="space-y-1.5">
      {label && (
        <label htmlFor={fieldId} className="block text-[13px] font-medium text-fg-base">
          {label}
          {required && <span className="ml-0.5 text-danger">*</span>}
        </label>
      )}
      {kids}
      {error ? (
        <p className="text-[12.5px] leading-snug text-danger-fg">{error}</p>
      ) : hint ? (
        <p className="text-[12.5px] leading-snug text-fg-subtle">{hint}</p>
      ) : null}
    </div>
  )
}

// Inputs/Select/Textarea inherit handoff's `.select-shell` / `.search-shell`
// look — 36px tall, hairline border, focus ring in accent-subtle.
const controlBase =
  'block w-full h-9 rounded-md border bg-bg-surface px-3 text-[14px] text-fg-base ' +
  'border-border-base placeholder:text-fg-subtle ' +
  'transition-colors focus-visible:outline-none focus-visible:border-accent ' +
  'focus-visible:shadow-[0_0_0_3px_var(--accent-subtle)] ' +
  'disabled:cursor-not-allowed disabled:opacity-60'

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  invalid?: boolean
  leadingIcon?: ReactNode
  trailingIcon?: ReactNode
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, invalid, leadingIcon, trailingIcon, ...rest },
  ref,
) {
  if (leadingIcon || trailingIcon) {
    return (
      <div className="relative">
        {leadingIcon && (
          <div className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3 text-fg-subtle">
            {leadingIcon}
          </div>
        )}
        <input
          ref={ref}
          className={cn(
            controlBase,
            Boolean(leadingIcon) && 'pl-9',
            Boolean(trailingIcon) && 'pr-9',
            invalid && 'border-danger focus-visible:border-danger',
            className,
          )}
          {...rest}
        />
        {trailingIcon && (
          <div className="absolute inset-y-0 right-0 flex items-center pr-3 text-fg-subtle">
            {trailingIcon}
          </div>
        )}
      </div>
    )
  }
  return (
    <input
      ref={ref}
      className={cn(
        controlBase,
        invalid && 'border-danger focus-visible:border-danger',
        className,
      )}
      {...rest}
    />
  )
})

interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, invalid, rows = 4, ...rest },
  ref,
) {
  return (
    <textarea
      ref={ref}
      rows={rows}
      className={cn(
        'block w-full rounded-md border bg-bg-surface px-3 py-2 text-[13px] text-fg-base',
        'border-border-base placeholder:text-fg-subtle font-mono',
        'transition-colors focus-visible:outline-none focus-visible:border-accent',
        'focus-visible:shadow-[0_0_0_3px_var(--accent-subtle)]',
        'disabled:cursor-not-allowed disabled:opacity-60',
        invalid && 'border-danger focus-visible:border-danger',
        className,
      )}
      {...rest}
    />
  )
})

interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, invalid, children, ...rest },
  ref,
) {
  return (
    <select
      ref={ref}
      className={cn(
        controlBase,
        'pr-7',
        invalid && 'border-danger focus-visible:border-danger',
        className,
      )}
      {...rest}
    >
      {children}
    </select>
  )
})
