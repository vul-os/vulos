/**
 * apps/calendar/src/CalendarApp.jsx — Vulos OS calendar app wrapper
 *
 * Bridges the @vulos/office-client calendar + contacts libraries into the OS shell:
 *   - Reads the OS theme from ThemeProvider (useTheme hook)
 *   - Routes sign-out to the OS logout handler
 *   - Routes in-app notifications to the OS notification bus
 */

import { useState } from 'react'
import { CalendarLib } from '@vulos/office-client/calendar'
import { ContactsLib } from '@vulos/office-client/contacts'
import { useTheme } from '../../../src/core/ThemeProvider.jsx'

function osNotifier(title, body, priority = 'normal') {
  window.dispatchEvent(new CustomEvent('vulos:notification', {
    detail: {
      source: 'calendar',
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

const TABS = ['Calendar', 'Contacts']

export default function CalendarApp() {
  const { resolved: theme } = useTheme()
  const [activeTab, setActiveTab] = useState('Calendar')

  // osNotifier / osLogout are stable module-level functions; pass them through
  // directly rather than memoising (no captured render-scope state).
  const handleNotification = osNotifier
  const handleSignOut = osLogout

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <nav style={{
        display: 'flex',
        gap: '0.25rem',
        padding: '0.4rem 0.75rem',
        borderBottom: '1px solid var(--border, #1e1e1e)',
        background: 'var(--surface, #111)',
        flexShrink: 0,
      }}>
        {TABS.map(tab => (
          <button
            key={tab}
            data-testid={`tab-${tab.toLowerCase()}`}
            onClick={() => setActiveTab(tab)}
            style={{
              padding: '0.3rem 0.75rem',
              borderRadius: '6px',
              border: 'none',
              cursor: 'pointer',
              fontSize: '0.8125rem',
              fontWeight: activeTab === tab ? 600 : 400,
              background: activeTab === tab ? 'var(--accent-muted, rgba(15,106,108,0.15))' : 'transparent',
              color: activeTab === tab ? 'var(--accent, #0f6a6c)' : 'var(--text-faint, #888)',
            }}
          >
            {tab}
          </button>
        ))}
      </nav>
      <div style={{ flex: 1, overflow: 'hidden' }}>
        {activeTab === 'Calendar' ? (
          <CalendarLib
            apiBase="/api"
            theme={theme}
            onSignOut={handleSignOut}
            onNotification={handleNotification}
          />
        ) : (
          <ContactsLib
            apiBase="/api"
            theme={theme}
            onSignOut={handleSignOut}
            onNotification={handleNotification}
          />
        )}
      </div>
    </div>
  )
}
