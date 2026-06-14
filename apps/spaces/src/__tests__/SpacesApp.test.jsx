/**
 * SpacesApp OS wrapper tests
 * 1. Renders with default props
 * 2. Signs out
 * 3. Routes notifications
 * 4. Reads theme
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, fireEvent } from '@testing-library/react'

vi.mock('@vulos/office-client/spaces', () => ({
  SpacesLib: vi.fn(({ onSignOut, theme, onNotification }) => (
    <div data-testid="spaces-lib" data-theme={theme}>
      <button data-testid="mock-signout" onClick={onSignOut}>Sign out</button>
      <button data-testid="mock-notify" onClick={() => onNotification?.('New message', 'Hi', 'normal')}>Notify</button>
    </div>
  )),
}))

vi.mock('../../../src/core/ThemeProvider.jsx', () => ({
  useTheme: vi.fn(() => ({ resolved: 'dark' })),
}))

import SpacesApp from '../SpacesApp.jsx'

describe('SpacesApp OS wrapper', () => {
  beforeEach(() => {
    delete window.location
    window.location = { href: '' }
  })

  it('renders with default props and passes theme', async () => {
    await act(async () => { render(<SpacesApp />) })
    const el = screen.getByTestId('spaces-lib')
    expect(el).toBeTruthy()
    expect(el.getAttribute('data-theme')).toBe('dark')
  })

  it('onSignOut navigates to /auth/logout', async () => {
    await act(async () => { render(<SpacesApp />) })
    fireEvent.click(screen.getByTestId('mock-signout'))
    expect(window.location.href).toBe('/auth/logout')
  })

  it('onNotification dispatches vulos:notification with source=spaces', async () => {
    const events = []
    window.addEventListener('vulos:notification', e => events.push(e.detail))
    await act(async () => { render(<SpacesApp />) })
    fireEvent.click(screen.getByTestId('mock-notify'))
    window.removeEventListener('vulos:notification', e => events.push(e.detail))
    expect(events.length).toBe(1)
    expect(events[0].source).toBe('spaces')
  })

  it('reads OS theme from useTheme hook', async () => {
    await act(async () => { render(<SpacesApp />) })
    expect(screen.getByTestId('spaces-lib').getAttribute('data-theme')).toBe('dark')
  })
})
