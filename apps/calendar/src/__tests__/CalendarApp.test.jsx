/**
 * CalendarApp OS wrapper tests
 * 1. Renders with default props
 * 2. Signs out
 * 3. Routes notifications
 * 4. Reads theme
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, fireEvent } from '@testing-library/react'

vi.mock('@vulos/office-client/calendar', () => ({
  CalendarLib: vi.fn(({ onSignOut, theme, onNotification }) => (
    <div data-testid="calendar-lib" data-theme={theme}>
      <button data-testid="mock-signout" onClick={onSignOut}>Sign out</button>
      <button data-testid="mock-notify" onClick={() => onNotification?.('Event created', 'Team sync', 'normal')}>Notify</button>
    </div>
  )),
}))
vi.mock('@vulos/office-client/contacts', () => ({
  ContactsLib: vi.fn(() => <div data-testid="contacts-lib" />),
}))

vi.mock('../../../src/core/ThemeProvider.jsx', () => ({
  useTheme: vi.fn(() => ({ resolved: 'dark' })),
}))

import CalendarApp from '../CalendarApp.jsx'

describe('CalendarApp OS wrapper', () => {
  beforeEach(() => {
    delete window.location
    window.location = { href: '' }
  })

  it('renders with default props showing Calendar tab', async () => {
    await act(async () => { render(<CalendarApp />) })
    expect(screen.getByTestId('calendar-lib')).toBeTruthy()
  })

  it('onSignOut navigates to /auth/logout', async () => {
    await act(async () => { render(<CalendarApp />) })
    fireEvent.click(screen.getByTestId('mock-signout'))
    expect(window.location.href).toBe('/auth/logout')
  })

  it('onNotification dispatches vulos:notification with source=calendar', async () => {
    const events = []
    window.addEventListener('vulos:notification', e => events.push(e.detail))
    await act(async () => { render(<CalendarApp />) })
    fireEvent.click(screen.getByTestId('mock-notify'))
    window.removeEventListener('vulos:notification', e => events.push(e.detail))
    expect(events.length).toBe(1)
    expect(events[0].source).toBe('calendar')
    expect(events[0].title).toBe('Event created')
  })

  it('reads OS theme from useTheme hook', async () => {
    await act(async () => { render(<CalendarApp />) })
    expect(screen.getByTestId('calendar-lib').getAttribute('data-theme')).toBe('dark')
  })
})
