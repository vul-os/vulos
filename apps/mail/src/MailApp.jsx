/**
 * apps/mail/src/MailApp.jsx — Vulos OS mail app wrapper
 *
 * Bridges the @vulos/mail-client library into the OS shell:
 *   - Reads the OS theme from ThemeProvider (useTheme hook)
 *   - Routes sign-out to the OS logout handler via ShellProvider
 *   - Routes in-app notifications to the OS notification bus
 *     (window.dispatchEvent 'vulos:notification' custom event — same bus
 *      used by Toasts.jsx for websocket notifications)
 */

import { useCallback } from 'react'
// In production builds (apps/mail/ vite build) this resolves via package.json → file: link.
// In the vulos shell tests, vitest resolves it via the alias in vulos/vitest.config.js.
import { VulosMail } from '@vulos/mail-client'
import { useTheme } from '../../../src/core/ThemeProvider.jsx'

/**
 * osNotifier — routes mail notifications to the OS notification bus.
 * Dispatches the same CustomEvent shape that Toasts.jsx listens to on the
 * WebSocket channel, except we use a dedicated event name so the shell can
 * handle them in-process without needing a round-trip through the server.
 *
 * @param {string} title
 * @param {string} body
 * @param {'normal'|'high'} priority
 */
function osNotifier(title, body, priority = 'normal') {
  window.dispatchEvent(new CustomEvent('vulos:notification', {
    detail: {
      source: 'mail',
      title,
      body: body || '',
      priority,
      level: priority === 'high' ? 'urgent' : 'info',
    },
  }))
}

/**
 * osLogout — called when the user hits "Sign out" inside the mail client.
 * In OS mode we navigate to the auth logout endpoint which clears the
 * Vulos session cookie (the same cookie that grants JMAP access).
 */
function osLogout() {
  window.location.href = '/auth/logout'
}

export default function MailApp() {
  const { resolved: theme } = useTheme()

  const handleNotification = useCallback(osNotifier, [])
  const handleSignOut = useCallback(osLogout, [])

  return (
    <VulosMail
      apiBase="/api/jmap"
      theme={theme}
      onSignOut={handleSignOut}
      onNotification={handleNotification}
    />
  )
}
