/**
 * src/shells/OfficeShell.jsx — office.vulos.org standalone shell
 *
 * Top nav: Docs / Sheets / Slides / PDF
 * Routes: / /docs/:id /sheets/:id /slides/:id /pdf/:id
 *
 * All four app bundles are code-split and loaded lazily.
 * Wrapped in RequireAuth — redirects to app.vulos.org/login on 401.
 *
 * Deploy: dist-office/  SPA fallback — server must serve index.html for all
 * unmatched paths (fly.io: [[http_service]] with force_https + spa routing).
 */

import { lazy, Suspense, useState } from 'react'
import { Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import RequireAuth from './RequireAuth.jsx'

const DocsEditor   = lazy(() => import('../apps/docs/DocsEditor.jsx'))
const SheetsEditor = lazy(() => import('../apps/sheets/SheetsEditor.jsx'))
const SlidesEditor = lazy(() => import('../apps/slides/SlidesEditor.jsx'))
const PDFEditor    = lazy(() => import('../apps/pdf/PDFEditor.jsx'))

const TABS = [
  { id: 'docs',   label: 'Docs',   path: '/docs' },
  { id: 'sheets', label: 'Sheets', path: '/sheets' },
  { id: 'slides', label: 'Slides', path: '/slides' },
  { id: 'pdf',    label: 'PDF',    path: '/pdf' },
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
  const active = TABS.find(t => pathname.startsWith(t.path))?.id ?? 'docs'

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

export default function OfficeShell() {
  return (
    <RequireAuth>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', background: 'var(--bg, #0f0f0f)' }}>
        <TopNav />
        <div style={{ flex: 1, overflow: 'hidden' }}>
          <Suspense fallback={<Loading />}>
            <Routes>
              <Route path="/" element={<Navigate to="/docs" replace />} />
              <Route path="/docs" element={<DocsEditor />} />
              <Route path="/docs/:id" element={<DocsEditor />} />
              <Route path="/sheets" element={<SheetsEditor />} />
              <Route path="/sheets/:id" element={<SheetsEditor />} />
              <Route path="/slides" element={<SlidesEditor />} />
              <Route path="/slides/:id" element={<SlidesEditor />} />
              <Route path="/pdf" element={<PDFEditor />} />
              <Route path="/pdf/:id" element={<PDFEditor />} />
              <Route path="*" element={<Navigate to="/docs" replace />} />
            </Routes>
          </Suspense>
        </div>
      </div>
    </RequireAuth>
  )
}
