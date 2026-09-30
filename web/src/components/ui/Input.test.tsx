import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Field, Input } from './Input'

afterEach(cleanup)

describe('Field', () => {
  it('links its label and hint to the control', () => {
    render(<Field label="Email" hint="Use your invitation address"><Input type="email" /></Field>)

    const input = screen.getByRole('textbox', { name: 'Email' })

    expect(input.getAttribute('aria-describedby')).toBe(screen.getByText('Use your invitation address').id)
  })

  it('links an error and preserves an existing description', () => {
    render(
      <>
        <p id="existing-help">Existing help</p>

        <Field label="Confirm password" error="Passwords do not match">
          <Input type="password" aria-describedby="existing-help" />
        </Field>
      </>,
    )

    const input = screen.getByLabelText('Confirm password')
    const error = screen.getByText('Passwords do not match')

    expect(input.getAttribute('aria-describedby')?.split(' ')).toEqual(['existing-help', error.id])
    expect(input.getAttribute('aria-invalid')).toBe('true')
  })

  it('marks the control as invalid when an error is shown', () => {
    render(
      <Field label="Email" error="Invalid email">
        <Input type="email" />
      </Field>,
    )

    const input = screen.getByRole('textbox', { name: 'Email' })
    expect(input.className).toContain('border-danger')
  })

  it('associates the label when the control supplies its own id', () => {
    render(<Field label="Alias"><Input id="chosen-id" /></Field>)

    expect(screen.getByRole('textbox', { name: 'Alias' }).id).toBe('chosen-id')
  })

  it('uses the Field id when both parent and control specify one', () => {
    render(<Field id="field-id" label="Model"><Input id="old-id" /></Field>)

    expect(screen.getByRole('textbox', { name: 'Model' }).id).toBe('field-id')
  })
})