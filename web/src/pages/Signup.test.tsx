import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import Signup from './Signup'

vi.mock('../api/client', () => ({
  api: { getInvite: vi.fn() },
  ApiError: class ApiError extends Error {},
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function renderSignup() {
  render(
    <MemoryRouter initialEntries={['/signup/example']}>
      <Routes>
        <Route path="/signup/:token" element={<Signup onLogin={() => {}} />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('Signup', () => {
  it('announces invite validation as a loading status', () => {
    vi.mocked(api.getInvite).mockImplementation(() => new Promise(() => {}))
    renderSignup()

    expect(screen.getByRole('status').textContent).toBe('Validating invite…')
  })

  it('announces a failed invite validation as an alert', async () => {
    vi.mocked(api.getInvite).mockRejectedValue(new Error('invalid invite'))
    renderSignup()

    expect((await screen.findByRole('alert')).textContent).toBe('invalid invite')
  })
})
