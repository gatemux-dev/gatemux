import { describe, expect, it } from 'vitest'
import { adminNavigation, isNavCurrent, navigationFor } from './navigation'
import type { Principal } from './auth'
import type { AuthenticatedUser } from './types'

function principalWith(role: string, team_slug?: string): Principal {
  return {
    token: '',
    isMasterKey: false,
    user: { id: 1, email: 'a@example.com', name: 'A', is_admin: false, role, team_slug } as AuthenticatedUser,
  }
}

describe('navigationFor', () => {
  it('gives admins the full navigation', () => {
    expect(navigationFor(principalWith('admin'))).toBe(adminNavigation)
  })

  it('links a manager to their own team when they have one', () => {
    const groups = navigationFor(principalWith('manager', 'acme'))
    const links = groups.flatMap((g) => g.items.map((i) => i.to))
    expect(links).toContain('/teams/acme')
  })

  it('omits the team link for a manager without a team', () => {
    const groups = navigationFor(principalWith('manager'))
    const links = groups.flatMap((g) => g.items.map((i) => i.to))
    expect(links.some((l) => l.startsWith('/teams/'))).toBe(false)
  })

  it('gives members only the personal workspace', () => {
    const groups = navigationFor(principalWith('member'))
    expect(groups).toHaveLength(1)
    const links = groups[0].items.map((i) => i.to)
    expect(links).toEqual(['/account', '/keys', '/usage', '/playground'])
  })
})

describe('isNavCurrent', () => {
  it('matches exact and nested paths', () => {
    expect(isNavCurrent('/teams', '/teams')).toBe(true)
    expect(isNavCurrent('/teams', '/teams/acme')).toBe(true)
    expect(isNavCurrent('/teams', '/teams-other')).toBe(false)
  })

  it('keeps Settings highlighted on the setup wizard', () => {
    expect(isNavCurrent('/settings', '/setup')).toBe(true)
  })

  it('gives policy pages their own entries instead of lighting Settings', () => {
    expect(isNavCurrent('/settings', '/guardrails')).toBe(false)
    expect(isNavCurrent('/guardrails', '/guardrails')).toBe(true)
    expect(isNavCurrent('/settings', '/providers')).toBe(true)
  })
})
