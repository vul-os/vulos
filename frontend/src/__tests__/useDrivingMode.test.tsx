import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, cleanup, waitFor } from '@testing-library/react'

// Driving mode used to silence notifications ONLY by POSTing box-wide DND, as
// `fetch(...).catch(() => {})`. That endpoint is admin-only (DND-SCOPE-01), and
// fetch does not reject on a 403 — it resolves — so the catch never ran and the
// failure was invisible. A non-admin driver got driving-mode styling with every
// notification still arriving. These tests pin the local mute, which is real for
// every profile and needs no permission.

let mockProfile: { layout?: string; role?: string } | null = null
vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: mockProfile }),
}))

import { useDrivingMode } from '../core/useDrivingMode'
import { getPrefs, setMuted } from '../core/notificationStore'

function Probe() {
  const { isDriving } = useDrivingMode()
  return <span data-testid="d">{String(isDriving)}</span>
}

// 403 is what a non-admin actually receives. Note it RESOLVES — that is the
// whole point: a rejecting stub would not reproduce the bug.
function mockDnd(status: number) {
  const spy = vi.fn(() => Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve({}) }))
  vi.stubGlobal('fetch', spy)
  return spy
}

beforeEach(() => { mockProfile = null; setMuted(false); try { localStorage.removeItem("vulos.driving.muted") } catch { /* none */ } })
afterEach(() => { cleanup(); vi.unstubAllGlobals(); setMuted(false) })

describe('useDrivingMode — driving actually silences something', () => {
  it('mutes this browser for a NON-ADMIN whose box-wide DND is refused', async () => {
    mockProfile = { layout: 'car', role: 'user' }
    const fetchSpy = mockDnd(403)
    render(<Probe />)
    await waitFor(() => expect(getPrefs().muted).toBe(true))
    // The box-wide escalation is still attempted — it is just no longer the
    // only thing standing between the driver and a buzzing phone.
    expect(fetchSpy).toHaveBeenCalled()
  })

  it('mutes this browser for an admin too, alongside the box-wide call', async () => {
    mockProfile = { layout: 'driving', role: 'admin' }
    mockDnd(200)
    render(<Probe />)
    await waitFor(() => expect(getPrefs().muted).toBe(true))
  })

  it('does not mute when the profile is not a driving layout', async () => {
    mockProfile = { layout: 'pc', role: 'admin' }
    mockDnd(200)
    render(<Probe />)
    await new Promise(r => setTimeout(r, 20))
    expect(getPrefs().muted).toBe(false)
  })

  it('restores the mute on exit only when driving mode set it', async () => {
    mockProfile = { layout: 'car', role: 'user' }
    mockDnd(403)
    const { unmount } = render(<Probe />)
    await waitFor(() => expect(getPrefs().muted).toBe(true))
    unmount()
    cleanup()

    // Leaving driving mode unmutes, because we were the one who muted.
    mockProfile = { layout: 'pc', role: 'user' }
    mockDnd(403)
    render(<Probe />)
    await waitFor(() => expect(getPrefs().muted).toBe(false))
  })

  it('leaves a mute the USER set alone when driving mode ends', async () => {
    // Muted before driving started: driving mode must not "restore" someone
    // into being un-muted when they never asked to be.
    setMuted(true)
    mockProfile = { layout: 'car', role: 'user' }
    mockDnd(403)
    const { unmount } = render(<Probe />)
    await waitFor(() => expect(getPrefs().muted).toBe(true))
    unmount()
    cleanup()

    mockProfile = { layout: 'pc', role: 'user' }
    mockDnd(403)
    render(<Probe />)
    await new Promise(r => setTimeout(r, 20))
    expect(getPrefs().muted).toBe(true)
  })
})
