import { useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { LogOut, Menu, Moon, Search, Sun, X } from 'lucide-react'
import { clearSession, principalRole, type Principal } from '../auth'
import { api } from '../api/client'
import { useTheme } from '../theme'
import { isNavCurrent, navigationFor, type NavGroup } from '../navigation'
import BrandMark from './BrandMark'
import CommandPalette from './CommandPalette'

export default function Layout({ principal, children }: { principal: Principal; children: React.ReactNode }) {
  const loc = useLocation()
  const [mobileOpen, setMobileOpen] = useState(false)
  const drawer = useRef<HTMLDialogElement>(null)
  const nav = navigationFor(principal)
  const [paletteOpen, setPaletteOpen] = useState(false)

  // ⌘K / Ctrl+K opens the command menu from anywhere in the console.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  useEffect(() => { setMobileOpen(false) }, [loc.pathname, loc.search])
  useEffect(() => {
    if (!mobileOpen) { drawer.current?.close(); return }
    drawer.current?.showModal()
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { document.body.style.overflow = previousOverflow }
  }, [mobileOpen])

  return (
    <div className="shell density-cozy">
      <a className="skip-link" href="#main-content">Skip to content</a>
      <dialog ref={drawer} className="mobile-drawer" aria-label="Navigation menu"
        onCancel={() => setMobileOpen(false)} onClose={() => setMobileOpen(false)}
        onClick={(event) => { if (event.target === event.currentTarget) setMobileOpen(false) }}>
        <SidebarShell principal={principal} nav={nav} currentPath={loc.pathname} onSearch={() => { setMobileOpen(false); setPaletteOpen(true) }} onSelect={() => setMobileOpen(false)}>
          <button className="foot-theme" onClick={() => setMobileOpen(false)} aria-label="Close menu"><X size={18} /></button>
        </SidebarShell>
      </dialog>
      <div className="desktop-sidebar">
        <SidebarShell principal={principal} nav={nav} currentPath={loc.pathname} onSearch={() => setPaletteOpen(true)} />
      </div>
      <main className="main" id="main-content" tabIndex={-1}>
        <div className="mobile-topbar">
          <button onClick={() => setMobileOpen(true)} aria-label="Open menu" aria-haspopup="dialog" aria-expanded={mobileOpen} className="foot-theme"><Menu size={18} /></button>
          <span className="topbar-brand"><BrandMark size={20} /> GateMux</span>
        </div>
        {children}
      </main>
      <CommandPalette principal={principal} open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  )
}

function SidebarShell({ principal, nav, currentPath, onSelect, onSearch, children }: {
  principal: Principal; nav: NavGroup[]; currentPath: string; onSelect?: () => void; onSearch: () => void; children?: React.ReactNode
}) {
  const { theme, toggle } = useTheme()
  const role = principalRole(principal)
  const [signingOut, setSigningOut] = useState(false)
  return (
    <aside className="sidebar">
      <div className="sidebar-brand-row">
        <Link to="/" className="sidebar-brand" onClick={onSelect}>
          <BrandMark size={22} />
          <span className="brand-name">GateMux</span>
        </Link>
        {children}
      </div>
      <button type="button" className="sidebar-search" onClick={onSearch}>
        <Search size={14} aria-hidden="true" />
        <span>Search</span>
        <kbd>{/Mac|iPhone|iPad/.test(navigator.platform) ? '⌘K' : 'Ctrl K'}</kbd>
      </button>
      <nav className="sidebar-nav" aria-label="Primary">
        {nav.map((group, groupIndex) => (
          <div className="nav-group" key={group.label || groupIndex}>
            {group.label && <h2 className="nav-group-label">{group.label}</h2>}
            {group.items.map((item) => {
              const active = isNavCurrent(item.to, currentPath)
              const Icon = item.icon
			  if (item.external) return <a key={item.to} href={item.to} className="navlink" onClick={onSelect}><Icon size={16} aria-hidden="true" /><span>{item.label}</span></a>
              return <Link key={item.to} to={item.to} onClick={onSelect} aria-current={active ? 'page' : undefined}
                className={'navlink' + (active ? ' is-current' : '')}>
                <Icon size={16} aria-hidden="true" /><span>{item.label}</span>
              </Link>
            })}
          </div>
        ))}
      </nav>
      <div className="sidebar-foot">
        <div className="foot-identity">
          <span className="foot-avatar" aria-hidden="true">{initials(principal.isMasterKey ? 'Admin' : principal.user?.name || principal.user?.email || '?')}</span>
          <span className="foot-text">
            <span className="foot-name">{principal.isMasterKey ? 'Admin session' : principal.user?.name || principal.user?.email}</span>
            <span className="foot-sub">{principal.isMasterKey ? 'Master key' : role.charAt(0).toUpperCase() + role.slice(1)}</span>
          </span>
        </div>
        <button className="foot-theme" onClick={toggle} aria-label="Toggle theme" title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}>
          {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
        </button>
        <button className="foot-theme" disabled={signingOut} aria-label="Sign out" title="Sign out" onClick={async () => {
          setSigningOut(true)
          await api.logout().catch(() => {})
          clearSession()
        }}><LogOut size={16} /></button>
      </div>
    </aside>
  )
}

function initials(name: string): string {
  return name.split(/[\s@.]+/).filter(Boolean).slice(0, 2).map((p) => p[0]).join('').toUpperCase()
}
