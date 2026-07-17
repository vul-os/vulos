/**
 * console/Layout.jsx — the authenticated console shell (sidebar + topbar + drawer).
 *
 * Adapted from vulos-cloud's src/pages/app/Layout.jsx onto the management app's
 * basename-aware router (router.jsx / useRouter): every nav link is an
 * app-relative href rendered through toFullPath() with `data-router`, so the
 * hand-rolled router intercepts the click and navigates client-side under the
 * /console basename — no full reload, session preserved.
 *
 * Structure (unchanged from cloud):
 *   Left  — sticky 232px rail: brand, grouped nav, status/docs footer.
 *   Right — content column: slim topbar (hamburger · breadcrumb · docs · avatar)
 *           + routed page in the single scroller (.vkl-page-area).
 * Mobile (≤1024px): rail → drawer behind the hamburger (scroll-lock, Esc-close,
 * link-close). All styling lives in ./console.css (the instrument-panel layer).
 *
 * The billing nav GROUP is shown only when the commercial seam is enabled
 * (see nav.js) — the OSS management build shows a purely operational console.
 */

import { useState, useEffect, useCallback, useRef } from 'react'
import { LogoMark } from '../components/Logo.jsx'
import { useAuth } from '../auth/AuthProvider.jsx'
import { useRouter, toFullPath } from '../router.jsx'
import { clientReplace } from '../auth/nav.js'
import { commercial } from '@vulos/commercial-seam'
import { buildNavGroups, isActive, findCrumbs } from './nav.js'
import { useOperator } from './admin/useAdmin.js'
import { Icon } from './icons.jsx'
import './console.css'

/* ─── Sidebar nav link ───────────────────────────────────────────────────── */
function SidebarLink({ href, label, icon, path, onClick }) {
  const active = isActive(href, path)
  return (
    <a
      href={toFullPath(href)}
      data-router
      onClick={onClick}
      className={`vkl-nav-link${active ? ' active' : ''}`}
      aria-current={active ? 'page' : undefined}
    >
      <span className="vkl-nav-link-icon"><Icon name={icon} /></span>
      <span className="vkl-nav-link-label">{label}</span>
      {active && <span className="vkl-nav-link-bar" aria-hidden="true" />}
    </a>
  )
}

/* ─── Sidebar (shared: desktop rail + mobile drawer) ─────────────────────── */
function SidebarContent({ path, onLinkClick, isOperator }) {
  const groups = buildNavGroups({ isOperator })
  return (
    <div className="vkl-sidebar-inner">
      <a
        href={toFullPath('/')}
        data-router
        className="vkl-sidebar-brand"
        onClick={onLinkClick}
        aria-label="Vulos console — dashboard"
      >
        <LogoMark size={18} tone="on-dark" />
        <span className="vkl-sidebar-brand-word">Vulos</span>
        <span className="vkl-sidebar-brand-chip">Console</span>
      </a>

      {/* Not a <nav>: the enclosing <aside aria-label="Console navigation"> is
          the landmark. */}
      <div className="vkl-sidebar-nav">
        {groups.map((group) => (
          <div key={group.label} className="vkl-nav-group">
            <span className="vkl-nav-group-label">{group.label}</span>
            <ul className="vkl-nav-group-items" role="list">
              {group.items.map((item) => (
                <li key={item.href}>
                  <SidebarLink
                    href={item.href}
                    label={item.label}
                    icon={item.icon}
                    path={path}
                    onClick={onLinkClick}
                  />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>

      <div className="vkl-sidebar-foot">
        <a href={toFullPath('/account-status')} data-router className="vkl-sidebar-foot-link" onClick={onLinkClick}>
          <span className="vkl-sidebar-foot-dot" aria-hidden="true" />
          status
        </a>
        <span className="vkl-sidebar-foot-link" style={{ color: 'var(--text-ghost)' }}>
          seam: {commercial.enabled ? 'commercial' : 'oss'}
        </span>
      </div>
    </div>
  )
}

/* ─── User menu ──────────────────────────────────────────────────────────── */
function UserMenu() {
  const { user, logout } = useAuth()
  const [open, setOpen] = useState(false)
  const menuRef = useRef(null)
  const triggerRef = useRef(null)

  const email = user?.email ?? user?.handle ?? 'account'
  const initial = String(email).charAt(0).toUpperCase()
  const close = useCallback(() => setOpen(false), [])

  useEffect(() => {
    if (!open) return
    function handler(e) {
      if (menuRef.current && !menuRef.current.contains(e.target) &&
          triggerRef.current && !triggerRef.current.contains(e.target)) close()
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open, close])

  useEffect(() => {
    if (!open) return
    function handler(e) { if (e.key === 'Escape') close() }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [open, close])

  async function handleLogout() {
    close()
    await logout()
    clientReplace('/login')
  }

  return (
    <div className="vkl-usermenu-wrap">
      <button
        ref={triggerRef}
        className="vkl-avatar"
        aria-label="Account menu"
        aria-expanded={open}
        aria-haspopup="true"
        onClick={() => setOpen((v) => !v)}
      >
        {initial}
      </button>
      {open && (
        <div ref={menuRef} className="vkl-usermenu-dropdown" role="menu" aria-label="Account menu">
          <div className="vkl-usermenu-header">
            <span className="vkl-usermenu-email">{email}</span>
          </div>
          <div className="vkl-usermenu-sep" aria-hidden="true" />
          <a href={toFullPath('/account-status')} data-router className="vkl-usermenu-item" role="menuitem" onClick={close}>
            Account status
          </a>
          <a href={toFullPath('/account/social')} data-router className="vkl-usermenu-item" role="menuitem" onClick={close}>
            Connected accounts
          </a>
          <div className="vkl-usermenu-sep" aria-hidden="true" />
          <button className="vkl-usermenu-item vkl-usermenu-signout" role="menuitem" onClick={handleLogout}>
            Sign out
          </button>
        </div>
      )}
    </div>
  )
}

/* ════════════════════════════════════════════════════════════════════════════
   LAYOUT
════════════════════════════════════════════════════════════════════════════ */
export default function Layout({ children }) {
  const { path } = useRouter()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const { isOperator } = useOperator()
  const crumbs = findCrumbs(path, { isOperator })
  const closeDrawer = useCallback(() => setDrawerOpen(false), [])

  // Close the drawer whenever the route changes (link-close).
  useEffect(() => { setDrawerOpen(false) }, [path])

  // Body-scroll-lock while the drawer is open.
  useEffect(() => {
    document.body.style.overflow = drawerOpen ? 'hidden' : ''
    return () => { document.body.style.overflow = '' }
  }, [drawerOpen])

  // Escape closes the drawer.
  useEffect(() => {
    if (!drawerOpen) return
    function handler(e) { if (e.key === 'Escape') closeDrawer() }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [drawerOpen, closeDrawer])

  return (
    <>
      {drawerOpen && (
        <div className="vkl-drawer-overlay open" aria-hidden="true" onClick={closeDrawer} />
      )}

      <div className={`vkl-drawer${drawerOpen ? ' open' : ''}`} role="dialog" aria-modal="true" aria-label="Console navigation" id="vkl-app-drawer">
        <SidebarContent path={path} onLinkClick={closeDrawer} isOperator={isOperator} />
      </div>

      <div className="vkl-shell">
        <aside className="vkl-sidebar" aria-label="Console navigation">
          <SidebarContent path={path} onLinkClick={null} isOperator={isOperator} />
        </aside>

        <div className="vkl-content-col">
          <header className="vkl-topbar">
            <button
              className={`vkl-hamburger${drawerOpen ? ' open' : ''}`}
              aria-label={drawerOpen ? 'Close navigation' : 'Open navigation'}
              aria-expanded={drawerOpen}
              aria-controls="vkl-app-drawer"
              onClick={() => setDrawerOpen((v) => !v)}
            >
              <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true">
                {drawerOpen ? (
                  <><line x1="3" y1="3" x2="15" y2="15" /><line x1="15" y1="3" x2="3" y2="15" /></>
                ) : (
                  <><line x1="2" y1="5" x2="16" y2="5" /><line x1="2" y1="9.5" x2="13" y2="9.5" /><line x1="2" y1="14" x2="16" y2="14" /></>
                )}
              </svg>
            </button>

            <a href={toFullPath('/')} data-router className="vkl-topbar-brand" aria-label="Vulos console — dashboard">
              <LogoMark size={20} tone="on-dark" />
              <span className="vkl-sidebar-brand-word">Vulos</span>
            </a>

            <div className="vkl-crumbs" aria-hidden="true">
              <span className="vkl-crumbs-eyebrow">{crumbs.group}</span>
              <span className="vkl-crumbs-sep">/</span>
              <span className="vkl-crumbs-title">{crumbs.page}</span>
            </div>

            <div className="vkl-topbar-spacer" />
            <a href="/docs" className="vkl-topbar-link">Docs</a>
            <UserMenu />
          </header>

          <main className="vkl-page-area" id="vkl-main-content">
            {children}
          </main>
        </div>
      </div>
    </>
  )
}
