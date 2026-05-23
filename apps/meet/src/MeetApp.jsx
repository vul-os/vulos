/**
 * apps/meet/src/MeetApp.jsx — Vulos OS meet app wrapper
 *
 * Bridges the @vulos/office-client spaces library (meeting/call components)
 * into the OS shell:
 *   - Reads the OS theme from ThemeProvider (useTheme hook)
 *   - Routes sign-out to the OS logout handler
 *   - Routes in-app notifications to the OS notification bus
 *
 * In the OS shell, "meet" is a lightweight launch-and-join — no sidebar nav.
 * The SpacesLib MemoryRouter starts at /meet for the focused join flow.
 */

import { useCallback } from 'react'
import { SpacesLib } from '@vulos/office-client/spaces'
import { useTheme } from '../../../src/core/ThemeProvider.jsx'

function osNotifier(title, body, priority = 'normal') {
  window.dispatchEvent(new CustomEvent('vulos:notification', {
    detail: {
      source: 'meet',
      title,
      body: body || '',
      priority,
      level: priority === 'high' ? 'urgent' : 'info',
    },
  }))
}

function osLogout() {
  window.location.href = '/auth/logout'
}

export default function MeetApp() {
  const { resolved: theme } = useTheme()

  const handleNotification = useCallback(osNotifier, [])
  const handleSignOut = useCallback(osLogout, [])

  return (
    <SpacesLib
      apiBase="/api"
      theme={theme}
      onSignOut={handleSignOut}
      onNotification={handleNotification}
      initialChannel="__meet__"
    />
  )
}
