/**
 * apps/office/src/OfficeApp.jsx — Vulos OS office app wrapper
 *
 * Bridges the @vulos/office-client library (DocsApp + SheetsApp + SlidesApp + PDFApp)
 * into the OS shell:
 *   - Reads the OS theme from ThemeProvider (useTheme hook)
 *   - Routes sign-out to the OS logout handler
 *   - Routes in-app notifications to the OS notification bus
 *     (window.dispatchEvent 'vulos:notification' custom event)
 */

import { useTheme } from '../../../src/core/ThemeProvider.jsx'

// Import the docs app as the default pane. The OS shell opens specific app
// windows (office/sheets/slides/pdf) as separate window instances — each
// window imports the appropriate lib directly. This wrapper is the default
// "Office" icon in the launchpad and opens DocsApp as the landing pane.
import { DocsApp } from '@vulos/office-client/docs'

function osNotifier(title, body, priority = 'normal') {
  window.dispatchEvent(new CustomEvent('vulos:notification', {
    detail: {
      source: 'office',
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

export default function OfficeApp() {
  const { resolved: theme } = useTheme()

  // osNotifier / osLogout are stable module-level functions; pass them through
  // directly rather than memoising (no captured render-scope state).
  const handleNotification = osNotifier
  const handleSignOut = osLogout

  return (
    <DocsApp
      apiBase="/api"
      theme={theme}
      onSignOut={handleSignOut}
      onNotification={handleNotification}
    />
  )
}
