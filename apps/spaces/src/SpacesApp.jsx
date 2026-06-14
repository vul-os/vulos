/**
 * apps/spaces/src/SpacesApp.jsx — Vulos OS spaces app wrapper
 *
 * Bridges the @vulos/office-client spaces library into the OS shell:
 *   - Reads the OS theme from ThemeProvider (useTheme hook)
 *   - Routes sign-out to the OS logout handler
 *   - Routes in-app notifications to the OS notification bus
 *
 * All calls use the P2P/WebRTC mesh path. LiveKit SFU support has been
 * removed. The mesh path supports up to DEFAULT_MESH_THRESHOLD participants.
 */

import { useCallback } from 'react'
import { SpacesLib } from '@vulos/office-client/spaces'
import { useTheme } from '../../../src/core/ThemeProvider.jsx'

function osNotifier(title, body, priority = 'normal') {
  window.dispatchEvent(new CustomEvent('vulos:notification', {
    detail: {
      source: 'spaces',
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

export default function SpacesApp() {
  const { resolved: theme } = useTheme()

  const handleNotification = useCallback(osNotifier, [])
  const handleSignOut = useCallback(osLogout, [])

  return (
    <SpacesLib
      apiBase="/api"
      theme={theme}
      onSignOut={handleSignOut}
      onNotification={handleNotification}
    />
  )
}
