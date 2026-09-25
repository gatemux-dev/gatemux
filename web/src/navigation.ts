import { Activity, Bell, BookOpen, Boxes, CircleDollarSign, Gauge, History, KeyRound, LayoutDashboard, MessageSquare, Settings, ShieldCheck, User, UserCog, Users, type LucideIcon } from 'lucide-react'
import type { Principal } from './auth'
import { principalRole } from './auth'

export type NavItem = { to: string; label: string; icon: LucideIcon; external?: boolean }
export type NavGroup = { label: string; items: NavItem[] }

// Grouped by the job: watch traffic, manage who and what, set the rules
// that apply to every request, and configure the gateway itself. Provider
// health and passthroughs stay as tabs inside Settings.
export const adminNavigation: NavGroup[] = [
  { label: '', items: [
    { to: '/overview', label: 'Overview', icon: LayoutDashboard },
    { to: '/usage', label: 'Logs', icon: Activity },
    { to: '/spend', label: 'Usage & spend', icon: CircleDollarSign },
    { to: '/playground', label: 'Playground', icon: MessageSquare },
  ] },
  { label: 'Manage', items: [
    { to: '/models', label: 'Models', icon: Boxes },
    { to: '/teams', label: 'Teams', icon: Users },
    { to: '/users', label: 'Users', icon: UserCog },
  ] },
  { label: 'Policies', items: [
    { to: '/guardrails', label: 'Guardrails', icon: ShieldCheck },
    { to: '/concurrency', label: 'Limits', icon: Gauge },
    { to: '/alerts', label: 'Alerts', icon: Bell },
  ] },
  { label: 'System', items: [
    { to: '/settings', label: 'Settings', icon: Settings },
    { to: '/audit', label: 'Audit log', icon: History },
    { to: '/docs', label: 'API reference', icon: BookOpen, external: true },
  ] },
]

export function navigationFor(principal: Principal): NavGroup[] {
  const role = principalRole(principal)
  if (role === 'admin') return adminNavigation
  if (role === 'manager') return [
    { label: '', items: [
      ...(principal.user?.team_slug ? [{ to: `/teams/${encodeURIComponent(principal.user.team_slug)}`, label: 'My team', icon: Users }] : []),
      { to: '/usage', label: 'Logs', icon: Activity },
      { to: '/spend', label: 'Usage & spend', icon: CircleDollarSign },
      { to: '/playground', label: 'Playground', icon: MessageSquare },
    ] },
    { label: '', items: [{ to: '/account', label: 'Account', icon: User }] },
  ]
  return [{ label: '', items: [
    { to: '/account', label: 'Account', icon: User },
    { to: '/keys', label: 'My keys', icon: KeyRound },
    { to: '/usage', label: 'My usage', icon: Activity },
    { to: '/playground', label: 'Playground', icon: MessageSquare },
  ] }]
}

// Routes that render inside another nav item's area keep that item lit,
// so the sidebar always answers "where am I".
const SETTINGS_FAMILY = ['/settings', '/providers', '/passthroughs', '/setup']

export function isNavCurrent(to: string, pathname: string): boolean {
  if (to === '/settings') return SETTINGS_FAMILY.some((p) => pathname === p || pathname.startsWith(`${p}/`))
  if (to === '/users' && (pathname === '/invites' || pathname.startsWith('/invites/'))) return true
  return pathname === to || pathname.startsWith(`${to}/`)
}
