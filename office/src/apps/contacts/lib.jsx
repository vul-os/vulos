/**
 * src/apps/contacts/lib.jsx — @vulos/office-client contacts library entry
 *
 * Exports <ContactsLib /> — the Contacts app as a single embeddable React component.
 *
 * Props:
 *   apiBase        {string}    — base URL for API (default '' = same-origin)
 *   theme          {string}    — 'light' | 'dark' | 'auto' (default 'auto')
 *   onSignOut      {function}  — callback when user hits sign-out
 *   onNotification {function}  — optional (title, body, priority) => void
 */

import { Suspense, lazy } from 'react'
import { MemoryRouter, Routes, Route, Navigate } from 'react-router-dom'

const ContactsApp = lazy(() => import('./ContactsApp.jsx'))

export function ContactsLib({
  apiBase = '',
  theme = 'auto',
  onSignOut,
  onNotification,
}) {
  return (
    <div data-theme={theme} style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <MemoryRouter initialEntries={['/contacts']}>
        <Suspense fallback={<div style={{ flex: 1 }} />}>
          <Routes>
            <Route path="/contacts" element={<ContactsApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="/contacts/:id" element={<ContactsApp apiBase={apiBase} onNotification={onNotification} onSignOut={onSignOut} />} />
            <Route path="*" element={<Navigate to="/contacts" replace />} />
          </Routes>
        </Suspense>
      </MemoryRouter>
    </div>
  )
}

export default ContactsLib
