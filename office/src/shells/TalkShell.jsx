/**
 * src/shells/TalkShell.jsx — talk.vulos.org standalone shell (Spaces)
 *
 * Full Spaces experience: channels, DMs, threads, calls, meetings.
 * Routes: / /channels/:id /dm/:id /room/:id /meet/:id
 *
 * Wrapped in RequireAuth — redirects to app.vulos.org/login on 401.
 *
 * Deploy: dist-talk/  SPA fallback — server must serve index.html for all
 * unmatched paths.
 */

import { lazy, Suspense } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import RequireAuth from './RequireAuth.jsx'

const SpacesApp = lazy(() => import('../apps/spaces/SpacesApp.jsx'))
const Room      = lazy(() => import('../apps/spaces/Room.jsx'))
const Meetings  = lazy(() => import('../apps/spaces/Meetings.jsx'))

function Loading() {
  return (
    <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--bg, #0f0f0f)' }}>
      <div className="w-7 h-7 border-2 border-accent border-t-transparent rounded-full animate-spin" />
    </div>
  )
}

export default function TalkShell() {
  return (
    <RequireAuth>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', background: 'var(--bg, #0f0f0f)' }}>
        <Suspense fallback={<Loading />}>
          <Routes>
            <Route path="/" element={<SpacesApp />} />
            <Route path="/channels/:id" element={<SpacesApp />} />
            <Route path="/dm/:id" element={<SpacesApp />} />
            <Route path="/room/:id" element={<Room />} />
            <Route path="/meet/:id" element={<Meetings />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </div>
    </RequireAuth>
  )
}
