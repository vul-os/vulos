/**
 * App.jsx — vulos-management authed SPA shell (phase 1 scaffold).
 *
 * Renders the base app chrome — top bar + left sidebar nav + content area —
 * around a routed page. This is the FOUNDATION only: a minimal authed shell and
 * a placeholder Dashboard that proves the commercial seam works (it renders
 * <BillingSlot/>, which is NoOp/empty in this OSS build). The real auth flow and
 * console pages migrate in phase 2 — do not add them here yet.
 *
 * Design system: theme.css is copied verbatim from the marketing site
 * (vulos-cloud), so colours/spacing/type are identical to vulos.org. Inter is
 * self-hosted via @fontsource (see theme.css) — air-gappable, no Google Fonts.
 */

import { useRouter, toFullPath } from './router.jsx'
import { useAuth } from './auth/AuthProvider.jsx'
import { clientReplace } from './auth/nav.js'
import Dashboard from './pages/Dashboard.jsx'
import { commercial } from '@vulos/commercial-seam'

/* ─── Sidebar nav model ───────────────────────────────────────────────────────
   Placeholder groups for phase 1. Real routes wire in phase 2. The "Account"
   group is where commercial affordances (billing/usage/invoices) will surface
   IF the seam is enabled — in OSS they simply render nothing. */
const NAV_GROUPS = [
  {
    label: 'Overview',
    items: [{ path: '/', label: 'Dashboard' }],
  },
  {
    label: 'Operate',
    items: [
      { path: '/fleet', label: 'Fleet', soon: true },
      { path: '/devices', label: 'Devices', soon: true },
    ],
  },
  {
    label: 'Account',
    items: [
      { path: '/account', label: 'Account', soon: true },
      // Only advertised when a commercial seam is wired; hidden in OSS.
      ...(commercial.enabled ? [{ path: '/billing', label: 'Billing' }] : []),
    ],
  },
]

/* ─── Routed pages (phase 1: Dashboard only) ─────────────────────────────── */
function renderPage(path) {
  switch (path) {
    case '/':
      return <Dashboard />
    default:
      return (
        <div className="vm-empty">
          <h2>Not built yet</h2>
          <p>
            <code>{path}</code> is a phase-2 page. This is the phase-1 scaffold —
            sign-in and the real console migrate next.
          </p>
        </div>
      )
  }
}

function Sidebar({ path }) {
  return (
    <aside className="vm-sidebar" aria-label="Console navigation">
      <div className="vm-brand">
        <span className="vm-brand-mark" aria-hidden="true">◇</span>
        <span className="vm-brand-name">Vulos</span>
        <span className="vm-brand-sub">Management</span>
      </div>
      <nav className="vm-nav">
        {NAV_GROUPS.map((group) => (
          <div className="vm-nav-group" key={group.label}>
            <div className="vm-nav-group-label">{group.label}</div>
            {group.items.map((item) => {
              const active = item.path === path
              return (
                <a
                  key={item.path}
                  href={toFullPath(item.path)}
                  data-router
                  className={'vm-nav-link' + (active ? ' is-active' : '')}
                  aria-current={active ? 'page' : undefined}
                >
                  {item.label}
                  {item.soon && <span className="vm-nav-soon">soon</span>}
                </a>
              )
            })}
          </div>
        ))}
      </nav>
      <div className="vm-sidebar-foot">
        <span className="vm-seam-badge">
          seam: {commercial.enabled ? 'commercial' : 'oss'}
        </span>
      </div>
    </aside>
  )
}

function TopBar() {
  const { user, logout } = useAuth()

  const handleSignOut = async () => {
    await logout()
    // Session dropped — return to the focused sign-in surface.
    clientReplace('/login')
  }

  return (
    <header className="vm-topbar">
      <div className="vm-topbar-title">Console</div>
      <div className="vm-topbar-actions">
        {user && (
          <span className="vm-user" title="Signed-in account">
            {user.email || user.handle || 'account'}
          </span>
        )}
        <button type="button" className="vm-signout" onClick={handleSignOut}>
          Sign out
        </button>
      </div>
    </header>
  )
}

export default function App() {
  const { path } = useRouter()
  return (
    <div className="vm-shell">
      <Sidebar path={path} />
      <div className="vm-main">
        <TopBar />
        <main className="vm-content">{renderPage(path)}</main>
      </div>
    </div>
  )
}
