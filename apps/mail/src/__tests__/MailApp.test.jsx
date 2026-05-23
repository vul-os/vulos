/**
 * MailApp — OS wrapper tests
 *
 * Tests that:
 *   1. MailApp renders without crashing (mocking the library + OS providers)
 *   2. The onSignOut callback wires to the OS logout path
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, fireEvent } from '@testing-library/react'

// ─── Mock the mail-client library ─────────────────────────────────────────────

vi.mock('@vulos/mail-client', () => ({
  VulosMail: vi.fn(({ onSignOut, theme, apiBase, onNotification }) => (
    <div data-testid="vulosmail-lib" data-theme={theme} data-api={apiBase}>
      <button data-testid="mock-signout" onClick={onSignOut}>Sign out</button>
      <button
        data-testid="mock-notify"
        onClick={() => onNotification?.('New mail', 'You have 1 new message')}
      >
        Notify
      </button>
    </div>
  )),
}))

// ─── Mock the OS ThemeProvider ────────────────────────────────────────────────

vi.mock('../../../src/core/ThemeProvider.jsx', () => ({
  useTheme: vi.fn(() => ({ resolved: 'dark' })),
}))

import MailApp from '../MailApp.jsx'

describe('MailApp OS wrapper', () => {
  beforeEach(() => {
    // Reset location mock
    delete window.location
    window.location = { href: '' }
  })

  it('renders without crashing and passes apiBase + theme to library', async () => {
    await act(async () => {
      render(<MailApp />)
    })

    const lib = screen.getByTestId('vulosmail-lib')
    expect(lib).toBeTruthy()
    expect(lib.getAttribute('data-api')).toBe('/api/jmap')
    expect(lib.getAttribute('data-theme')).toBe('dark')
  })

  it('onSignOut callback navigates to /auth/logout (OS logout)', async () => {
    await act(async () => {
      render(<MailApp />)
    })

    const btn = screen.getByTestId('mock-signout')
    fireEvent.click(btn)

    expect(window.location.href).toBe('/auth/logout')
  })

  it('onNotification dispatches vulos:notification custom event', async () => {
    const events = []
    const listener = (e) => events.push(e.detail)
    window.addEventListener('vulos:notification', listener)

    await act(async () => {
      render(<MailApp />)
    })

    const btn = screen.getByTestId('mock-notify')
    fireEvent.click(btn)

    window.removeEventListener('vulos:notification', listener)

    expect(events.length).toBe(1)
    expect(events[0].source).toBe('mail')
    expect(events[0].title).toBe('New mail')
  })
})
