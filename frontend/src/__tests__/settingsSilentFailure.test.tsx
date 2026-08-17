/**
 * settingsSilentFailure.test.tsx — Settings must not draw a dead backend as a
 * designed empty state, and must not report a write that never landed.
 *
 * Settings is the largest surface in the OS and had never been audited. The
 * audit found the same two shapes the rest of the OS was fixed for tonight,
 * repeated across roughly a dozen sections:
 *
 *   1. `fetch(p).then(r => r.json())` with no `res.ok`. Every /api service on
 *      this box answers 5xx with a JSON error body, so the error body PARSES,
 *      flows into a `toX()` narrower, and comes out a well-formed record of
 *      undefineds — which the section renders as a confident, wrong answer.
 *      A WiFi scan against a 500 rendered "No networks found — move closer to
 *      your router", i.e. it blamed the user's router for the box being down.
 *
 *   2. `fetch(…, {method:'POST'}).then(refresh)` with the response never looked
 *      at. A refused pair, a failed radio power-on, a WiFi password posted into
 *      a 403 and a "Backup Now" that never backed anything up were all
 *      indistinguishable from success.
 *
 * Each test below drives the REAL Settings component with a failing backend and
 * asserts the failure is VISIBLE. They are written against rendered text a user
 * can read, not internals, so they keep holding if the panels are restyled.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// Both exports are required: Settings pulls in ../desktop, whose store.ts
// imports resolveViewportLayout from this same module to pick a dock profile.
// A mock that returns only useViewport makes the whole file fail to load.
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

/**
 * The reply GET /api/bluetooth/status actually gives.
 *
 * The box serves `bluetooth.Status` (backend/services/bluetooth/bluetooth.go),
 * a struct whose `powered` field is a plain bool and is therefore ALWAYS on the
 * wire. `{}` is not a shape this route can produce, and standing one in matters
 * because the Bluetooth panel now distinguishes "the radio is off" from "the
 * box has not said" and DISABLES the switch for the second — a radio whose
 * state is unknown is not a radio you can be asked to toggle. A `{}` fixture
 * put the panel in that unknown state, so the click under test never reached
 * the write at all and the reported failure was the fixture's, not the code's.
 */
const BT_STATUS_OK = { powered: false, discoverable: false, discovering: false, name: 'box', address: 'AA:BB:CC:DD:EE:FF', devices: [] }

/**
 * A backend where the listed paths fail with a 500 carrying a JSON error body —
 * the exact shape that made these bugs invisible — and everything else is a
 * bland 200. `seen` records the writes so a test can also assert one happened.
 */
function backend(failing: string[], status = 500) {
  const seen: string[] = []
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    seen.push(`${init?.method || 'GET'} ${url}`)
    if (failing.some(f => url.includes(f))) {
      return Promise.resolve({
        ok: false,
        status,
        json: () => Promise.resolve({ error: 'bluetoothd is not running' }),
      })
    }
    if (url.includes('/api/bluetooth/status')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(BT_STATUS_OK) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return { seen, fetchMock }
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })
beforeEach(() => { vi.clearAllMocks() })

async function openSection(name: string) {
  const user = userEvent.setup()
  render(<Settings />)
  await user.click(await screen.findByRole('button', { name }))
  return user
}

describe('WiFi — a failing box is not a router problem', () => {
  it('a scan that 500s shows the failure and NOT the "No networks found" empty state', async () => {
    backend(['/api/wifi/scan'])
    const user = await openSection('WiFi')
    await user.click(await screen.findByRole('button', { name: 'Scan Networks' }))

    // The load-bearing half: the fabricated empty state must be gone. Before
    // the fix this text was on screen, advising the user to move closer to a
    // router that was never the problem.
    expect(await screen.findByRole('alert')).toHaveTextContent(/bluetoothd is not running/)
    expect(screen.queryByText('No networks found')).not.toBeInTheDocument()
  })

  it('a scan that genuinely returns zero networks still shows the empty state', async () => {
    // The complement, so the fix above cannot be "delete the empty state".
    vi.stubGlobal('fetch', vi.fn((url: string) =>
      Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(url.includes('/api/wifi/scan') ? [] : {}) })))
    const user = await openSection('WiFi')
    await user.click(await screen.findByRole('button', { name: 'Scan Networks' }))
    expect(await screen.findByText('No networks found')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('a refused connect keeps the dialog and the typed password instead of reporting success', async () => {
    // The worst of the family: this posted a network password and then closed
    // the dialog and cleared the field no matter what came back.
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/api/wifi/connect')) {
        return Promise.resolve({ ok: false, status: 403, json: () => Promise.resolve({ error: 'not permitted' }) })
      }
      if (url.includes('/api/wifi/scan')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([{ ssid: 'Home', bssid: 'aa', signal: -40, band: '5GHz', security: 'wpa2' }]) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    const user = await openSection('WiFi')
    await user.click(await screen.findByRole('button', { name: 'Scan Networks' }))
    await user.click(await screen.findByRole('button', { name: 'Connect to Home' }))

    const pw = await screen.findByLabelText('Password')
    await user.type(pw, 'hunter2')
    await user.click(screen.getByRole('button', { name: 'Connect' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not permitted/)
    // Still open, still holding what was typed — the user can retry rather than
    // walk away believing they joined the network.
    expect(screen.getByLabelText('Password')).toHaveValue('hunter2')
  })
})

describe('Bluetooth — six writes that never checked their response', () => {
  it('a failed radio power-on is reported, not swallowed', async () => {
    const { seen } = backend(['/api/bluetooth/power'])
    const user = await openSection('Bluetooth')
    await user.click(await screen.findByRole('switch', { name: 'Bluetooth' }))
    // The write must genuinely have been attempted. Without this, a switch
    // that is inert for any reason (disabled, unwired) would leave the alert
    // assertion below to be satisfied by some other failure on the panel.
    expect(seen).toContain('POST /api/bluetooth/power')
    expect(await screen.findByRole('alert')).toHaveTextContent(/bluetoothd is not running/)
  })

  // The complement of the fixture note above, and the reason these tests can
  // no longer be satisfied by a `{}` status: when the box has NOT said whether
  // the radio is on, the panel must not offer a toggle whose position would be
  // a guess. Without this, `disabled={powered === undefined}` could be deleted
  // and every other test here would stay green.
  it('a radio whose state the box never reported is shown as unknown, not as Off', async () => {
    backend(['/api/bluetooth/status'])
    await openSection('Bluetooth')
    const toggle = await screen.findByRole('switch', { name: 'Bluetooth' })
    expect(toggle).toBeDisabled()
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    // ...and it must not be drawn as a confident "Off" pill either.
    expect(await screen.findByRole('alert')).toHaveTextContent(/bluetoothd is not running/)
    expect(screen.queryByText('Off')).not.toBeInTheDocument()
  })

  it('a failed pair is reported, not swallowed', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/api/bluetooth/pair')) {
        return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'pairing rejected' }) })
      }
      if (url.includes('/api/bluetooth/status')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ powered: true, devices: [{ address: 'AA:BB', name: 'Keyboard', type: 'input' }] }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    const user = await openSection('Bluetooth')
    await user.click(await screen.findByRole('button', { name: 'Pair with Keyboard' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/pairing rejected/)
  })

  it('a status read that 500s does not render as a powered-off radio with no devices', async () => {
    backend(['/api/bluetooth/status'])
    await openSection('Bluetooth')
    expect(await screen.findByRole('alert')).toHaveTextContent(/bluetoothd is not running/)
  })
})

describe('writes whose failure the user most needs to see', () => {
  it('Backup & Sync: a failed "Backup Now" says so instead of reporting nothing', async () => {
    // The highest-stakes silent write in the file. A backup button that reports
    // nothing is a backup button the user believes in, and the failure would
    // otherwise only surface on the day they try to restore.
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/api/vault/backup')) {
        return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'S3 credentials rejected' }) })
      }
      if (url.includes('/api/vault/status')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ initialized: true }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    const user = await openSection('Backup & Sync')
    await user.click(await screen.findByRole('button', { name: 'Backup Now' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/S3 credentials rejected/)
  })

  it('Users & Profiles: a refused role change is reported', async () => {
    // A privilege operation that ignored its response: 403 to a non-admin and
    // success both produced a silent re-read of an unchanged list.
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      if (url.includes('/role')) {
        return Promise.resolve({ ok: false, status: 403, json: () => Promise.resolve({ error: 'not permitted' }) })
      }
      if (url.endsWith('/api/profiles') && (!init || (init.method ?? 'GET') === 'GET')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([{ user_id: 'u2', username: 'bob', display_name: 'Bob', role: 'user' }]) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    const user = await openSection('Users & Profiles')
    await user.selectOptions(await screen.findByLabelText('Role for Bob'), 'admin')
    expect(await screen.findByRole('alert')).toHaveTextContent(/not permitted/)
  })
})

describe('a button that claimed success for a write that failed', () => {
  it('Remote Access: a refused Save does not flash "Saved"', async () => {
    // The sharpest of the family. saveConfig read no status and set `saved`
    // unconditionally, so a 403 or 500 still showed "Saved" — not a quiet
    // failure but an active assertion that a write had happened.
    backend(['/api/network/configure'], 403)
    const user = await openSection('Remote Access')
    await user.click(await screen.findByRole('button', { name: /^Save$/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/bluetoothd is not running/)
    expect(screen.queryByRole('button', { name: 'Saved' })).not.toBeInTheDocument()
  })
})

describe('Display — a label that read "undefined"', () => {
  it('never renders a brightness percentage before the box has reported one', async () => {
    // The gate was `status?.brightness?.device !== 'none'`, which is TRUE while
    // status is still null, so first paint (and every failure) drew a slider
    // labelled literally "Brightness (undefined%)".
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {}))) // never resolves: first-paint state
    await openSection('Display')
    // The panel is genuinely on screen (its own heading, not the nav item), so
    // this cannot pass by measuring an unrendered section.
    expect(await screen.findByRole('heading', { name: 'Display' })).toBeInTheDocument()
    expect(screen.queryByText(/undefined/)).not.toBeInTheDocument()
  })
})

describe('a 200 carrying an error body is a failure, not a success', () => {
  it('reports a write the box answered 200 OK with {"error"} — res.ok alone believes it', async () => {
    // This shape is live in this codebase: POST /telephony/call and /sms/send
    // both answer {"error": …} with HTTP 200. A section that checks only
    // `res.ok` reports success for something that never happened, so the write
    // path checks the body as well.
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/api/bluetooth/power')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ error: 'no adapter present' }) })
      }
      if (url.includes('/api/bluetooth/status')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(BT_STATUS_OK) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    const user = await openSection('Bluetooth')
    await user.click(await screen.findByRole('switch', { name: 'Bluetooth' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/no adapter present/)
  })

  it('reports a non-2xx that carries NO error body at all', async () => {
    // Added because mutation-testing this file showed the `res.ok` check could
    // be DELETED with every other test still green: each of them fails with a
    // `{"error": …}` body, and the separate error-body check caught those on
    // its own. Nothing pinned the status code by itself.
    //
    // The uncovered case is real rather than theoretical — the box's own
    // handlers always write `{"error": …}`, but a reverse proxy or relay in
    // front of it answers 502 with an HTML page. `res.json()` then rejects,
    // the body is null, there is no error field, and without the status check
    // the write is silently reported as having succeeded.
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.includes('/api/bluetooth/power')) {
        return Promise.resolve({ ok: false, status: 502, json: () => Promise.reject(new Error('not JSON')) })
      }
      if (url.includes('/api/bluetooth/status')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(BT_STATUS_OK) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    const user = await openSection('Bluetooth')
    await user.click(await screen.findByRole('switch', { name: 'Bluetooth' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/502/)
  })
})
