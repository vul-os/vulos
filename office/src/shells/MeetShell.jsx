/**
 * src/shells/MeetShell.jsx — meet.vulos.org standalone shell
 *
 * Focused meeting join: enter meeting ID or follow link, join lobby, enter call.
 * No chat sidebar, no channel nav.
 *
 * Routes: / /meet/:id
 *
 * Wrapped in RequireAuth — redirects to app.vulos.org/login on 401.
 *
 * Deploy: dist-meet/  SPA fallback — server must serve index.html for all
 * unmatched paths.
 */

import { lazy, Suspense, useState } from 'react'
import { Routes, Route, Navigate, useNavigate, useParams } from 'react-router-dom'
import RequireAuth from './RequireAuth.jsx'

const Room     = lazy(() => import('../apps/spaces/Room.jsx'))
const CallView = lazy(() => import('../apps/spaces/CallView.jsx'))
const Meetings = lazy(() => import('../apps/spaces/Meetings.jsx'))

function Loading() {
  return (
    <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--bg, #0f0f0f)' }}>
      <div className="w-7 h-7 border-2 border-accent border-t-transparent rounded-full animate-spin" />
    </div>
  )
}

function MeetLanding() {
  const navigate = useNavigate()
  const [meetId, setMeetId] = useState('')

  const join = (e) => {
    e.preventDefault()
    const id = meetId.trim()
    if (id) navigate(`/meet/${id}`)
  }

  return (
    <div style={{
      flex: 1,
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      justifyContent: 'center',
      gap: '1.5rem',
      background: 'var(--bg, #0f0f0f)',
      padding: '2rem',
    }}>
      <h1 style={{ fontSize: '1.5rem', fontWeight: 700, color: 'var(--text, #f5f5f5)', margin: 0 }}>
        Join a meeting
      </h1>
      <form onSubmit={join} style={{ display: 'flex', gap: '0.75rem', width: '100%', maxWidth: '400px' }}>
        <input
          data-testid="meet-id-input"
          type="text"
          value={meetId}
          onChange={e => setMeetId(e.target.value)}
          placeholder="Enter meeting code"
          style={{
            flex: 1,
            padding: '0.6rem 0.9rem',
            borderRadius: '8px',
            border: '1px solid var(--border, #333)',
            background: 'var(--surface, #1a1a1a)',
            color: 'var(--text, #f5f5f5)',
            fontSize: '0.9rem',
            outline: 'none',
          }}
        />
        <button
          type="submit"
          data-testid="join-btn"
          style={{
            padding: '0.6rem 1.2rem',
            borderRadius: '8px',
            border: 'none',
            background: 'var(--accent, #0f6a6c)',
            color: '#fff',
            cursor: 'pointer',
            fontWeight: 600,
            fontSize: '0.9rem',
          }}
        >
          Join
        </button>
      </form>
    </div>
  )
}

function MeetRoom() {
  const { id } = useParams()
  // Reuse Meetings + Room components from the Spaces app
  return (
    <Suspense fallback={<Loading />}>
      <Room sessionId={id} />
    </Suspense>
  )
}

export default function MeetShell() {
  return (
    <RequireAuth>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', background: 'var(--bg, #0f0f0f)' }}>
        <Suspense fallback={<Loading />}>
          <Routes>
            <Route path="/" element={<MeetLanding />} />
            <Route path="/meet/:id" element={<MeetRoom />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </div>
    </RequireAuth>
  )
}
