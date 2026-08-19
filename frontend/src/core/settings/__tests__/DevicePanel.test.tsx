// DevicePanel.test.tsx — the home-screen row, before the device has answered.
//
// This panel talks to the Android host through nativeBridge, and a bridge call
// is not instant and can fail. The row for "is Vulos the home screen?" printed a
// definite answer while the question was still outstanding, and kept printing it
// for ever if the call rejected — refreshLauncher swallows that error, leaving
// the status null permanently.
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

interface LauncherStatus { ok: boolean; isDefault: boolean; canRequest: boolean }

// vi.mock's factory is hoisted above the module body, so the mutable handle the
// tests drive has to be created in a hoisted block too.
const h = vi.hoisted(() => ({ launcherStatus: new Promise<LauncherStatus>(() => {}) }))

vi.mock('../../nativeBridge', () => ({
  nativeBridge: {
    notify: { available: false, serviceStatus: () => Promise.resolve(false), enableService: vi.fn(), disableService: vi.fn() },
    launcher: {
      available: true,
      status: () => h.launcherStatus,
      setDefault: vi.fn(() => Promise.resolve({ ok: true })),
      openHomeSettings: vi.fn(),
    },
    biometric: { available: false },
  },
}))

import DevicePanel from '../DevicePanel'

beforeEach(() => { h.launcherStatus = new Promise(() => {}) })
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('DevicePanel — the home-screen row does not answer for the device', () => {
  it('says it is checking, not "Not set", before the bridge has answered', () => {
    // The call never settles: a slow host, or the rejection refreshLauncher
    // swallows, which leaves this state permanently.
    h.launcherStatus = new Promise(() => {})
    render(<DevicePanel />)

    expect(
      screen.queryByText('Not set'),
      'the panel reported the home screen was not set before the device had said so',
    ).toBeNull()
    // Two rows can legitimately be checking at once (the background-connection
    // row above uses the same wording), so this asserts presence, not identity;
    // the discriminating assertion is the one above.
    expect(screen.getAllByText('Checking…').length).toBeGreaterThan(0)
  })

  it('does not offer to set the home screen while it does not know if it may', () => {
    // canRequest is unknown here. Offering the action anyway is a button we
    // cannot say the device will accept.
    h.launcherStatus = new Promise(() => {})
    render(<DevicePanel />)

    expect(screen.getByRole('button', { name: /set as home screen/i })).toBeDisabled()
  })

  it('reports "Not set" once the device has actually said so', async () => {
    h.launcherStatus = Promise.resolve({ ok: true, isDefault: false, canRequest: true })
    render(<DevicePanel />)

    expect(await screen.findByText('Not set')).toBeTruthy()
    expect(screen.getByRole('button', { name: /set as home screen/i })).toBeEnabled()
  })

  it('reports "Active" when Vulos is the home screen', async () => {
    h.launcherStatus = Promise.resolve({ ok: true, isDefault: true, canRequest: true })
    render(<DevicePanel />)

    await waitFor(() => expect(screen.getByText('Active')).toBeTruthy())
    expect(screen.queryByRole('button', { name: /set as home screen/i })).toBeNull()
  })

  it('keeps the button disabled when the device says it may not be asked', async () => {
    h.launcherStatus = Promise.resolve({ ok: true, isDefault: false, canRequest: false })
    render(<DevicePanel />)

    expect(await screen.findByText('Not set')).toBeTruthy()
    expect(screen.getByRole('button', { name: /set as home screen/i })).toBeDisabled()
  })
})
