/**
 * src/shells/CalendarShell.jsx — calendar.vulos.org standalone shell
 *
 * Tabs: Calendar / Contacts
 * Routes: / /calendar /calendar/event/:id /contacts /contacts/:id
 *
 * Wrapped in RequireAuth — redirects to app.vulos.org/login on 401.
 *
 * Deploy: dist-calendar/  SPA fallback — server must serve index.html for all
 * unmatched paths.
 */

import { lazy, Suspense } from 'react'
import { Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import RequireAuth from './RequireAuth.jsx'

const CalendarApp  = lazy(() => import('../apps/calendar/CalendarApp.jsx'))
const ContactsApp  = lazy(() => import('../apps/contacts/ContactsApp.jsx'))

const TABS = [
  { id: 'calendar', label: 'Calendar', path: '/calendar' },
  { id: 'contacts', label: 'Contacts', path: '/contacts' },
]

function Loading() {
  return (
    <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <div className="w-7 h-7 border-2 border-accent border-t-transparent rounded-full animate-spin" />
    </div>
  )
}

function TopNav() {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const active = TABS.find(t => pathname.startsWith(t.path))?.id ?? 'calendar'

  return (
    <nav style={{
      display: 'flex',
      alignItems: 'center',
      gap: '0.25rem',
      padding: '0.5rem 1rem',
      borderBottom: '1px solid var(--border, #1e1e1e)',
      background: 'var(--surface, #111)',
      flexShrink: 0,
    }}>
      <a href="https://vulos.org" style={{ marginRight: '1rem', fontWeight: 600, fontSize: '0.9rem', color: 'var(--accent, #0f6a6c)', textDecoration: 'none' }}>
        Vulos
      </a>
      {TABS.map(tab => (
        <button
          key={tab.id}
          data-testid={`nav-${tab.id}`}
          onClick={() => navigate(tab.path)}
          style={{
            padding: '0.35rem 0.85rem',
            borderRadius: '6px',
            border: 'none',
            cursor: 'pointer',
            fontSize: '0.8125rem',
            fontWeight: active === tab.id ? 600 : 400,
            background: active === tab.id ? 'var(--accent-muted, rgba(15,106,108,0.15))' : 'transparent',
            color: active === tab.id ? 'var(--accent, #0f6a6c)' : 'var(--text-faint, #888)',
            transition: 'background 0.15s, color 0.15s',
          }}
        >
          {tab.label}
        </button>
      ))}
    </nav>
  )
}

export default function CalendarShell() {
  return (
    <RequireAuth>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', background: 'var(--bg, #0f0f0f)' }}>
        <TopNav />
        <div style={{ flex: 1, overflow: 'hidden' }}>
          <Suspense fallback={<Loading />}>
            <Routes>
              <Route path="/" element={<Navigate to="/calendar" replace />} />
              <Route path="/calendar" element={<CalendarApp />} />
              <Route path="/calendar/event/:id" element={<CalendarApp />} />
              <Route path="/contacts" element={<ContactsApp />} />
              <Route path="/contacts/:id" element={<ContactsApp />} />
              <Route path="*" element={<Navigate to="/calendar" replace />} />
            </Routes>
          </Suspense>
        </div>
      </div>
    </RequireAuth>
  )
}
