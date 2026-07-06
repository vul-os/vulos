/**
 * Settings.integration.test.jsx — wave-28 shell integration suite.
 *
 * Renders the REAL Settings app (desktop layout) and drives section navigation
 * → Notifications → the Do Not Disturb / sound toggles, which are wired to the
 * REAL notification store. Asserts the store pref actually flips (the settings
 * UI and the notificationStore share one source of truth, so this pins that
 * seam end-to-end). Heavy hardware/provider sub-panels are stubbed; the
 * notification prefs path is the shipping code.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// Desktop layout (static rail, not the mobile drawer).
vi.mock('../../shell/useViewport', () => ({ useViewport: () => 'desktop' }))
vi.mock('../../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: { display_name: 'Ada', role: 'user' }, updateProfile: vi.fn(), logout: vi.fn() }),
}))
vi.mock('../../core/ThemeProvider', () => ({ useTheme: () => ({}), DEFAULT_ACCENT: '#3b82f6' }))
vi.mock('../../core/useWallpaper.jsx', () => ({ useWallpaper: () => ({ wallpaper: null, setWallpaper: vi.fn() }), DEFAULT_WALLPAPER: '' }))
vi.mock('../../core/PublicAppsManager', () => ({ PamVisibilityControl: () => null }))
vi.mock('../../core/settings/AIRouterPanel.jsx', () => ({ default: () => <div>AI Router Panel</div> }))
vi.mock('../../core/settings/StoragePanel.jsx', () => ({ default: () => null }))
vi.mock('../../core/settings/PlanBillingPanel.jsx', () => ({ default: () => null }))
vi.mock('../../core/settings/DataExportPanel.jsx', () => ({ default: () => null }))

import Settings from '../../core/Settings.jsx'
import { getPrefs, __resetForTests } from '../../core/notificationStore.js'

beforeEach(() => { __resetForTests() })
afterEach(() => cleanup())

describe('Settings — navigation + notification prefs (integration)', () => {
  it('navigates from the section rail into the Notifications panel', async () => {
    const user = userEvent.setup()
    render(<Settings />)
    const nav = screen.getByRole('navigation', { name: 'Settings sections' })
    await user.click(await screen.findByRole('button', { name: 'Notifications' }))
    // The notifications panel copy appears.
    expect(await screen.findByText(/Do Not Disturb silences pop-ups/i)).toBeInTheDocument()
    expect(nav).toBeInTheDocument()
  })

  it('the Do Not Disturb toggle flips the real notification-store pref', async () => {
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: 'Notifications' }))
    const dnd = await screen.findByRole('switch', { name: 'Do Not Disturb' })
    expect(dnd).toHaveAttribute('aria-checked', 'false')
    expect(getPrefs().muted).toBe(false)

    await user.click(dnd)
    expect(getPrefs().muted).toBe(true)
    expect(dnd).toHaveAttribute('aria-checked', 'true')
  })

  it('the sound toggle flips the store sound pref', async () => {
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: 'Notifications' }))
    const sound = await screen.findByRole('switch', { name: 'Notification sounds' })
    expect(getPrefs().sound).toBe(true)
    await user.click(sound)
    expect(getPrefs().sound).toBe(false)
  })
})
