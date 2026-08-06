/**
 * SettingsMobile.test.jsx — WAVE-22 a11y / mobile polish
 *
 * On small screens the Settings sidebar collapses into a drawer opened from a
 * top bar. This covers the responsive contract added in wave-22:
 *
 *   • mobile: the section rail is replaced by a "Open settings sections" button
 *   • opening the drawer reveals the section list; picking a section closes it
 *   • Escape closes the drawer
 *   • the active section carries aria-current="page"
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// Force the mobile layout.
vi.mock('../shell/useViewport', () => ({ useViewport: () => 'mobile' }))

// Stub the auth + theme + wallpaper hooks and the heavy sub-panels so we can
// mount <Settings> in isolation.
vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: { display_name: 'T', role: 'user' }, updateProfile: vi.fn(), logout: vi.fn() }),
}))
vi.mock('../core/ThemeProvider', () => ({ useTheme: () => ({}), DEFAULT_ACCENT: '#3b82f6' }))
vi.mock('../core/useWallpaper.jsx', () => ({ useWallpaper: () => ({ wallpaper: null, setWallpaper: vi.fn() }), DEFAULT_WALLPAPER: '' }))
vi.mock('../core/PublicAppsManager', () => ({ PamVisibilityControl: () => null }))
vi.mock('../core/settings/StoragePanel.jsx', () => ({ default: () => null }))

import Settings from '../core/Settings'

// container.querySelector returns Element | null; the drawer tests assert the
// node exists before interacting with it, so this narrows honestly instead of
// asserting the type away.
function requireEl(container: HTMLElement, selector: string): Element {
  const el = container.querySelector(selector)
  if (!el) throw new Error(`expected element matching ${selector}`)
  return el
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({}) })))
})
afterEach(() => cleanup())

describe('Settings mobile drawer', () => {
  it('shows a section-drawer toggle instead of a static rail on mobile', async () => {
    render(<Settings />)
    expect(screen.getByRole('button', { name: /open settings sections/i })).toBeTruthy()
  })

  it('opens the drawer, navigates, and closes on selection', async () => {
    const user = userEvent.setup()
    const { container } = render(<Settings />)
    await user.click(screen.getByRole('button', { name: /open settings sections/i }))

    // Drawer nav (idPrefix "settings-drawer") is present with section buttons.
    const wifiBtn = container.querySelector('#settings-drawer-wifi')
    expect(wifiBtn).toBeTruthy()
    await user.click(requireEl(container, '#settings-drawer-wifi'))

    // Selecting a section closes the drawer (close button disappears).
    expect(screen.queryByRole('button', { name: /close settings sections/i })).toBeNull()
    // Top bar now reflects the active section.
    expect(screen.getByRole('button', { name: /open settings sections/i }).textContent).toMatch(/WiFi/)
  })

  it('closes the drawer on Escape', async () => {
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: /open settings sections/i }))
    expect(screen.getByRole('button', { name: /close settings sections/i })).toBeTruthy()
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(screen.queryByRole('button', { name: /close settings sections/i })).toBeNull()
  })

  it('marks the active section with aria-current="page"', async () => {
    const user = userEvent.setup()
    const { container } = render(<Settings />)
    await user.click(screen.getByRole('button', { name: /open settings sections/i }))
    const wifiBtn = container.querySelector('#settings-drawer-wifi')
    // Before selection, WiFi is not current.
    expect(wifiBtn).not.toHaveAttribute('aria-current')
    await user.click(requireEl(container, '#settings-drawer-wifi'))
    // Reopen and confirm WiFi is now current.
    await user.click(screen.getByRole('button', { name: /open settings sections/i }))
    expect(container.querySelector('#settings-drawer-wifi')).toHaveAttribute('aria-current', 'page')
  })
})
