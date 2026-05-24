/**
 * src/apps/spaces/lib.jsx — @vulos/office-client spaces library entry
 *
 * Exports <SpacesAppLib /> — the Spaces (chat + calls) app as a single
 * embeddable React component.
 *
 * Props:
 *   apiBase        {string}    — base URL for API (default '' = same-origin)
 *   theme          {string}    — 'light' | 'dark' | 'auto' (default 'auto')
 *   onSignOut      {function}  — callback when user hits sign-out
 *   onNotification {function}  — optional (title, body, priority) => void
 *   initialChannel {string}    — pre-open a channel/DM ID on mount
 */

import { Suspense, lazy } from 'react'
import { MemoryRouter, Routes, Route, Navigate } from 'react-router-dom'

const SpacesApp = lazy(() => import('./SpacesApp.jsx'))
const Room      = lazy(() => import('./Room.jsx'))
const Meetings  = lazy(() => import('./Meetings.jsx'))

export function SpacesLib({
  apiBase = '',
  theme = 'auto',
  onSignOut,
  onNotification,
  initialChannel,
}) {
  const initialPath = initialChannel ? `/channels/${initialChannel}` : '/spaces'
  return (
    <div data-theme={theme} style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Suspense fallback={<div style={{ flex: 1 }} />}>
          <Routes>
            <Route path="/spaces" element={<SpacesApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/channels/:id" element={<SpacesApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/dm/:id" element={<SpacesApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/room/:sessionId" element={<Room />} />
            <Route path="/meet" element={<Meetings apiBase={apiBase} />} />
            <Route path="*" element={<Navigate to="/spaces" replace />} />
          </Routes>
        </Suspense>
      </MemoryRouter>
    </div>
  )
}

export default SpacesLib
