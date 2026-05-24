/**
 * src/apps/calendar/lib.jsx — @vulos/office-client calendar library entry
 *
 * Exports <CalendarLib /> — the Calendar app as a single embeddable React component.
 *
 * Props:
 *   apiBase        {string}    — base URL for API (default '' = same-origin)
 *   theme          {string}    — 'light' | 'dark' | 'auto' (default 'auto')
 *   onSignOut      {function}  — callback when user hits sign-out
 *   onNotification {function}  — optional (title, body, priority) => void
 */

import { Suspense, lazy } from 'react'
import { MemoryRouter, Routes, Route, Navigate } from 'react-router-dom'

const CalendarApp = lazy(() => import('./CalendarApp.jsx'))

export function CalendarLib({
  apiBase = '',
  theme = 'auto',
  onSignOut,
  onNotification,
}) {
  return (
    <div data-theme={theme} style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <MemoryRouter initialEntries={['/calendar']}>
        <Suspense fallback={<div style={{ flex: 1 }} />}>
          <Routes>
            <Route path="/calendar" element={<CalendarApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/calendar/event/:id" element={<CalendarApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="*" element={<Navigate to="/calendar" replace />} />
          </Routes>
        </Suspense>
      </MemoryRouter>
    </div>
  )
}

export default CalendarLib
