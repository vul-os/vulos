/**
 * src/apps/slides/lib.jsx — @vulos/office-client slides library entry
 *
 * Exports <SlidesApp /> — the Slides editor as a single embeddable React component.
 *
 * Props:
 *   apiBase        {string}    — base URL for API (default '' = same-origin)
 *   theme          {string}    — 'light' | 'dark' | 'auto' (default 'auto')
 *   onSignOut      {function}  — callback when user hits sign-out
 *   onNotification {function}  — optional (title, body, priority) => void
 *   initialDocID   {string}    — pre-open a specific presentation on mount
 */

import { Suspense, lazy } from 'react'
import { MemoryRouter, Routes, Route, Navigate } from 'react-router-dom'

const SlidesEditor = lazy(() => import('./SlidesEditor.jsx'))

export function SlidesApp({
  apiBase = '',
  theme = 'auto',
  onSignOut,
  onNotification,
  initialDocID,
}) {
  const initialPath = initialDocID ? `/slides/${initialDocID}` : '/slides'
  return (
    <div data-theme={theme} style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Suspense fallback={<div style={{ flex: 1 }} />}>
          <Routes>
            <Route path="/slides/:id" element={<SlidesEditor apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/slides" element={<SlidesEditor apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="*" element={<Navigate to="/slides" replace />} />
          </Routes>
        </Suspense>
      </MemoryRouter>
    </div>
  )
}

export default SlidesApp
