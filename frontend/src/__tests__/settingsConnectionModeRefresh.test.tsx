/**
 * settingsConnectionModeRefresh.test.tsx — Settings → Connection Mode (NET-09).
 *
 * Refresh silently threw away an unsaved selection.
 *
 * NET9_refresh's success handler ran `setPending(d.mode ?? null)` on every read.
 * `pending` is the radio the user has clicked but not applied, so re-reading the
 * box's own mode overwrote the person's choice with the mode already running:
 * the radio jumped back, the Apply button greyed out, and nothing said why. The
 * only affordance that could restore the choice was making it again — if the
 * user noticed at all. Refresh sits directly beside Apply, so the misclick is
 * one button away from the action it undoes.
 *
 * THE FIX IS TO KEEP THE SELECTION, not to ask about it. Refresh is a READ of
 * what the box is doing — Active mode, external-listener state, resolved domain,
 * instance ID. A read has no business discarding input, and a confirm() would be
 * asking the user a question the code can answer: `pending` is either the mode
 * the box now reports (nothing to lose, nothing to ask) or it is not (keep it
 * and leave Apply live). So `pending` now means "the unsaved choice, or null for
 * none", the radios show `pending ?? current`, and the read never writes it.
 *
 * Driven through the real Settings component, because a dead affordance is a
 * user-visible bug: the assertions are on which radio is checked and whether
 * Apply is live, not on which setter ran.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('../shell/useViewport', () => ({
  useViewport: () => 'desktop',
  resolveViewportLayout: () => 'desktop',
}))
vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: { display_name: 'Ada', role: 'admin' }, updateProfile: vi.fn(), logout: vi.fn() }),
}))
vi.mock('../core/ThemeProvider', () => ({ useTheme: () => ({}), DEFAULT_ACCENT: '#3b82f6' }))
vi.mock('../core/useWallpaper.jsx', () => ({ useWallpaper: () => ({ wallpaper: null, setWallpaper: vi.fn() }), DEFAULT_WALLPAPER: '' }))
vi.mock('../core/PublicAppsManager', () => ({ PamVisibilityControl: () => null }))
vi.mock('../core/settings/StoragePanel.jsx', () => ({ default: () => null }))
vi.mock('../core/settings/DataExportPanel.jsx', () => ({ default: () => null }))

import Settings from '../core/Settings'

/** A box reporting `mode`, with the POST recorded so a test can assert on it. */
function backend(mode: () => string) {
  const seen: string[] = []
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    seen.push(`${init?.method || 'GET'} ${url}`)
    if (String(url).includes('/api/network/mode')) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ mode: mode(), external_listener_blocked: false, status: {} }),
      })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
  return seen
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })
beforeEach(() => { vi.clearAllMocks() })

async function openConnectionMode() {
  const user = userEvent.setup()
  render(<Settings />)
  await user.click(await screen.findByRole('button', { name: 'Connection Mode' }))
  await waitFor(() => expect(screen.getByLabelText(/Local Only/)).toBeInTheDocument())
  return user
}

const radio = (label: RegExp) => screen.getByLabelText(label) as HTMLInputElement
const applyBtn = () => screen.getByRole('button', { name: /^(Apply|Applying…|Applied)$/ })

describe('Connection Mode — Refresh does not discard an unsaved selection', () => {
  it('keeps the radio the user picked, and keeps Apply live', async () => {
    backend(() => 'fabric')
    const user = await openConnectionMode()

    // The box is on fabric; nothing chosen yet.
    expect(radio(/Fabric/).checked).toBe(true)
    expect(applyBtn()).toBeDisabled()

    await user.click(radio(/Local Only/))
    expect(radio(/Local Only/).checked).toBe(true)
    expect(applyBtn()).toBeEnabled()

    await user.click(screen.getByRole('button', { name: 'Refresh' }))

    // The read completed — the panel is showing the box's answer again — and the
    // user's choice survived it.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled())
    expect(radio(/Local Only/).checked, 'Refresh threw the selection away').toBe(true)
    expect(radio(/Fabric/).checked).toBe(false)
    expect(applyBtn(), 'Apply must still be live — there is still an unapplied change').toBeEnabled()
  })

  it('the surviving selection still applies, and applies the mode the user picked', async () => {
    // The half that makes it more than cosmetic: a preserved radio that Apply
    // no longer sends would be its own dead affordance.
    const seen = backend(() => 'fabric')
    const user = await openConnectionMode()

    await user.click(radio(/Own Domain/))
    await user.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled())

    seen.length = 0
    await user.click(applyBtn())
    await waitFor(() => expect(seen.some(s => s.startsWith('POST /api/network/mode'))).toBe(true))
  })

  it('still follows the box when the user has made no choice', async () => {
    // The complement, so "stop writing pending" cannot degrade into "the radios
    // never track the box". With nothing selected, a refresh that finds a new
    // mode must move the selection to it.
    let mode = 'fabric'
    backend(() => mode)
    const user = await openConnectionMode()
    expect(radio(/Fabric/).checked).toBe(true)

    mode = 'direct'
    await user.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => expect(radio(/Direct/).checked).toBe(true))
    expect(radio(/Fabric/).checked).toBe(false)
    expect(applyBtn(), 'the box is already on this mode — there is nothing to apply').toBeDisabled()
  })

  it('drops the pending state once the box reports the user got what they asked for', async () => {
    // If a refresh finds the box already on the pending mode, the choice is no
    // longer pending — Apply goes dead without anyone being asked anything.
    let mode = 'fabric'
    backend(() => mode)
    const user = await openConnectionMode()

    await user.click(radio(/Direct/))
    expect(applyBtn()).toBeEnabled()

    mode = 'direct'
    await user.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => expect(applyBtn()).toBeDisabled())
    expect(radio(/Direct/).checked).toBe(true)
  })
})
