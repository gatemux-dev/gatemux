import { describe, expect, it } from 'vitest'
import { principalRole, type Principal } from './auth'
import type { AuthenticatedUser } from './types'

function user(overrides: Partial<AuthenticatedUser>): AuthenticatedUser {
  return {
    id: 1,
    email: 'a@example.com',
    name: 'A',
    is_admin: false,
    ...overrides,
  } as AuthenticatedUser
}

function principal(overrides: Partial<Principal>): Principal {
  return { token: '', user: null, isMasterKey: false, ...overrides }
}

describe('principalRole', () => {
  it('treats a missing principal as member', () => {
    expect(principalRole(null)).toBe('member')
  })

  it('maps master-key principals to admin', () => {
    expect(principalRole(principal({ isMasterKey: true }))).toBe('admin')
  })

  it('uses the server role field when present', () => {
    expect(principalRole(principal({ user: user({ role: 'manager' }) }))).toBe('manager')
    expect(principalRole(principal({ user: user({ role: 'admin' }) }))).toBe('admin')
    expect(principalRole(principal({ user: user({ role: 'member' }) }))).toBe('member')
  })

  it('falls back to is_admin for pre-role sessions', () => {
    expect(principalRole(principal({ user: user({ role: undefined, is_admin: true }) }))).toBe('admin')
    expect(principalRole(principal({ user: user({ role: undefined, is_admin: false }) }))).toBe('member')
  })

  it('does not trust unknown role strings', () => {
    expect(principalRole(principal({ user: user({ role: 'superuser' as never }) }))).toBe('member')
  })
})
