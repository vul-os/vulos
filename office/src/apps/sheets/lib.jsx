/**
 * src/apps/sheets/lib.jsx — @vulos/office-client sheets library entry
 *
 * Exports <SheetsApp /> — the Sheets editor as a single embeddable React component.
 *
 * Props:
 *   apiBase        {string}    — base URL for API (default '' = same-origin)
 *   theme          {string}    — 'light' | 'dark' | 'auto' (default 'auto')
 *   onSignOut      {function}  — callback when user hits sign-out
 *   onNotification {function}  — optional (title, body, priority) => void
 *   initialDocID   {string}    — pre-open a specific sheet on mount
 */

import { Suspense, lazy } from 'react'
import { MemoryRouter, Routes, Route, Navigate } from 'react-router-dom'

const SheetsEditor = lazy(() => import('./SheetsEditor.jsx'))

export function SheetsApp({
  apiBase = '',
  theme = 'auto',
  onSignOut,
  onNotification,
  initialDocID,
}) {
  const initialPath = initialDocID ? `/sheets/${initialDocID}` : '/sheets'
  return (
    <div data-theme={theme} style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Suspense fallback={<div style={{ flex: 1 }} />}>
          <Routes>
            <Route path="/sheets/:id" element={<SheetsEditor apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/sheets" element={<SheetsEditor apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="*" element={<Navigate to="/sheets" replace />} />
          </Routes>
        </Suspense>
      </MemoryRouter>
    </div>
  )
}

export default SheetsApp
