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

// updateProfile's REAL contract (AuthProvider.tsx) is
// `Promise<'ok' | 'rejected' | 'unreachable'>` — it never throws, and Account's
// Save reads that word to decide between "Saved" and an error banner. A bare
// `vi.fn()` resolves to `undefined`, i.e. a fourth outcome the real provider
// cannot produce, so it exercised neither branch. `profileOutcome` lets a test
// choose which of the three real answers the box gives.
let profileOutcome: 'ok' | 'rejected' | 'unreachable' = 'ok'
const updateProfile = vi.fn(async () => profileOutcome)
vi.mock('../../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: { display_name: 'Ada', role: 'user' }, updateProfile, logout: vi.fn() }),
}))
vi.mock('../../core/ThemeProvider', () => ({ useTheme: () => ({}), DEFAULT_ACCENT: '#3b82f6' }))
vi.mock('../../core/useWallpaper.jsx', () => ({ useWallpaper: () => ({ wallpaper: null, setWallpaper: vi.fn() }), DEFAULT_WALLPAPER: '' }))
vi.mock('../../core/PublicAppsManager', () => ({ PamVisibilityControl: () => null }))
vi.mock('../../core/settings/StoragePanel.jsx', () => ({ default: () => null }))
vi.mock('../../core/settings/DataExportPanel.jsx', () => ({ default: () => null }))

import Settings from '../../core/Settings'
import { getPrefs, __resetForTests } from '../../core/notificationStore'

beforeEach(() => { __resetForTests(); profileOutcome = 'ok'; updateProfile.mockClear() })
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

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

// These three pin behaviour added in this pass — each closes a real gap
// (Account's Save button gave no feedback at all; a WiFi scan that found
// nothing rendered no different from "haven't scanned yet"; the Users &
// Profiles PIN setter had no failure path).
describe('Settings — save/empty-state feedback (integration)', () => {
  it('Account: Save shows Saving… then Saved', async () => {
    profileOutcome = 'ok'
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({}) })))
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: 'Account' }))
    const save = await screen.findByRole('button', { name: /^Save$/ })
    await user.click(save)
    expect(updateProfile).toHaveBeenCalled()
    expect(await screen.findByRole('button', { name: 'Saved' })).toBeInTheDocument()
  })

  // The other half of the same seam, and the reason the success case must NOT
  // be satisfied by a mock that resolves to anything at all: `updateProfile`
  // never throws, so before Account read its return value every one of the
  // three outcomes flashed "Saved". Asserting only the happy path leaves that
  // exact defect re-introducible — deleting the `outcome === 'ok'` check would
  // keep the test above green.
  it.each(['rejected', 'unreachable'] as const)(
    'Account: a %s write says so and never says Saved',
    async (outcome) => {
      profileOutcome = outcome
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({}) })))
      const user = userEvent.setup()
      render(<Settings />)
      await user.click(screen.getByRole('button', { name: 'Account' }))
      await user.click(await screen.findByRole('button', { name: /^Save$/ }))

      expect(await screen.findByRole('alert')).toHaveTextContent(
        outcome === 'rejected' ? /refused these details/i : /could not be reached/i,
      )
      expect(screen.queryByRole('button', { name: 'Saved' })).toBeNull()
    },
  )

  it('WiFi: scanning to zero results shows an empty state, not silence', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/api/wifi/scan')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
    }))
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: 'WiFi' }))
    await user.click(await screen.findByRole('button', { name: 'Scan Networks' }))
    expect(await screen.findByText('No networks found')).toBeInTheDocument()
  })

  it('Users & Profiles: the Lock Screen PIN setter reports success', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({}) })))
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: 'Users & Profiles' }))
    const pin = await screen.findByLabelText('Lock screen PIN')
    await user.type(pin, '1234')
    await user.click(screen.getByRole('button', { name: 'Set PIN' }))
    expect(await screen.findByText('PIN set')).toBeInTheDocument()
  })

  it('Users & Profiles: a failed PIN save reports the error, not silence', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })))
    const user = userEvent.setup()
    render(<Settings />)
    await user.click(screen.getByRole('button', { name: 'Users & Profiles' }))
    const pin = await screen.findByLabelText('Lock screen PIN')
    await user.type(pin, '1234')
    await user.click(screen.getByRole('button', { name: 'Set PIN' }))
    expect(await screen.findByText('boom')).toBeInTheDocument()
  })
})
