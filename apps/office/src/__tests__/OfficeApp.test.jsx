/**
 * OfficeApp OS wrapper tests
 * 1. Renders with default props
 * 2. Signs out via osLogout
 * 3. Routes notifications to vulos:notification bus
 * 4. Reads theme from useTheme
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, fireEvent } from '@testing-library/react'

vi.mock('@vulos/office-client/docs', () => ({
  DocsApp: vi.fn(({ onSignOut, theme, onNotification }) => (
    <div data-testid="docs-app" data-theme={theme}>
      <button data-testid="mock-signout" onClick={onSignOut}>Sign out</button>
      <button data-testid="mock-notify" onClick={() => onNotification?.('Saved', 'doc.docx', 'normal')}>Notify</button>
    </div>
  )),
}))
vi.mock('@vulos/office-client/sheets', () => ({
  SheetsApp: vi.fn(() => <div data-testid="sheets-app" />),
}))
vi.mock('@vulos/office-client/slides', () => ({
  SlidesApp: vi.fn(() => <div data-testid="slides-app" />),
}))
vi.mock('@vulos/office-client/pdf', () => ({
  PDFApp: vi.fn(() => <div data-testid="pdf-app" />),
}))

vi.mock('../../../src/core/ThemeProvider.jsx', () => ({
  useTheme: vi.fn(() => ({ resolved: 'dark' })),
}))

import OfficeApp from '../OfficeApp.jsx'

describe('OfficeApp OS wrapper', () => {
  beforeEach(() => {
    delete window.location
    window.location = { href: '' }
  })

  it('renders with default props and passes theme to library', async () => {
    await act(async () => { render(<OfficeApp />) })
    const el = screen.getByTestId('docs-app')
    expect(el).toBeTruthy()
    expect(el.getAttribute('data-theme')).toBe('dark')
  })

  it('onSignOut navigates to /auth/logout', async () => {
    await act(async () => { render(<OfficeApp />) })
    fireEvent.click(screen.getByTestId('mock-signout'))
    expect(window.location.href).toBe('/auth/logout')
  })

  it('onNotification dispatches vulos:notification with source=office', async () => {
    const events = []
    window.addEventListener('vulos:notification', e => events.push(e.detail))

    await act(async () => { render(<OfficeApp />) })
    fireEvent.click(screen.getByTestId('mock-notify'))

    window.removeEventListener('vulos:notification', e => events.push(e.detail))
    expect(events.length).toBe(1)
    expect(events[0].source).toBe('office')
    expect(events[0].title).toBe('Saved')
  })

  it('reads OS theme via useTheme', async () => {
    await act(async () => { render(<OfficeApp />) })
    expect(screen.getByTestId('docs-app').getAttribute('data-theme')).toBe('dark')
  })
})
