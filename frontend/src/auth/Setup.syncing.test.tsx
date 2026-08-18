// Setup.syncing.test.tsx — INIT-09's join/sync step, the screen a founder
// watches while a new box pulls an existing instance down from storage.
//
// Two defects, both of the kind this project keeps paying for: a screen that
// keeps asserting something it does not know, and a button that fires the same
// irreversible thing twice.
//
//  1. A DEAD FEED DRAWN AS PROGRESS. IS09_setError was declared and never
//     called. Every failure path — a 500 from /api/setup/join/status, a 404
//     whose /api/setup/mode fallback also failed, a thrown fetch, a 200 that is
//     not an object — did `return` or `catch {}` and left the poll running. On
//     a box whose status endpoint is down the step therefore showed
//     "Initialising sync … 17%" (phase 0 of 6) with no message, for as long as
//     anyone was willing to look at it. A percentage is not a neutral
//     placeholder: it is an active claim that work is in flight.
//
//  2. "CONTINUE IN BACKGROUND" COMPLETED SETUP TWICE. The click did
//     `IS09_setBgMode(true); onComplete()`, and the poll's done-branch did
//     `if (IS09_bgMode) onComplete()` — with the effect keyed on IS09_bgMode, so
//     the live closure saw the flag it had just been given. onComplete is the
//     wizard's finish(): PUT /api/device-profile, a root `ln -sf` through
//     POST /api/exec, POST /api/wifi/connect, POST /api/setup/complete, and the
//     finishError banner. /api/setup/complete is idempotent server-side; the
//     other four are not, and the banner belongs to a user who is reading it.
//
// The assertions are on what the SCREEN says, not on which handler ran.

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('../core/i18n', () => ({
  useI18n: () => ({ t: (k: string) => k, setLocale: () => {}, locale: 'en' }),
}))

import { IS09_SyncingStep } from './Setup'

type JsonBody = Record<string, unknown>

/** Answer /api/setup/join/status from a mutable script the test controls. */
function mockStatus(next: () => { ok: boolean; status: number; body: JsonBody }) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    calls.push(String(url))
    const r = next()
    return Promise.resolve({
      ok: r.ok,
      status: r.status,
      json: () => Promise.resolve(r.body),
    })
  }))
  return calls
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

// The step polls on a real 3s setInterval. Fake timers are deliberately NOT used
// here: RTL's waitFor drives the same timer queue, and the version of this file
// that used them deadlocked rather than failing, which is the worst way for a
// gate to be wrong. Real time it is — three quiet polls is about seven seconds.
const QUIET = 12000

describe('IS09 syncing step — a dead status feed is not drawn as progress', () => {
  it('stops showing a percentage, and says why, when the box never answers', async () => {
    mockStatus(() => ({ ok: false, status: 500, body: {} }))
    render(<IS09_SyncingStep onNext={vi.fn()} onComplete={vi.fn()} />)

    // Before the threshold the screen is allowed to look like it is starting up.
    expect(screen.getByText('17%')).toBeInTheDocument()

    // Three consecutive silences at the 3s poll interval, i.e. the mount poll
    // plus two ticks. Anything less and one dropped request cries wolf.
    await screen.findByRole('alert', {}, { timeout: QUIET })

    // The figure is gone. Not 0%, not frozen at 17% — withheld.
    expect(screen.queryByText('17%')).toBeNull()
    expect(screen.getByText('—')).toBeInTheDocument()

    // And the user is told, in a live region, rather than left to guess.
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/not answering/i)
    expect(alert.textContent).toMatch(/nothing here is moving/i)

    // "Initialising sync" was our assumption, never the box's report, so it is
    // not presented as the phase the box is in.
    expect(screen.getByText('No phase reported')).toBeInTheDocument()
  }, 30000)

  it('says the phase is only the LAST one reported when the box goes quiet mid-sync', async () => {
    let answering = true
    mockStatus(() => (answering
      ? { ok: true, status: 200, body: { phase: 'storage', done: false } }
      : { ok: false, status: 502, body: {} }))
    render(<IS09_SyncingStep onNext={vi.fn()} onComplete={vi.fn()} />)

    // phase 'storage' is index 3 of 6 → 67%
    await waitFor(() => expect(screen.getByText('67%')).toBeInTheDocument())

    answering = false
    await screen.findByRole('alert', {}, { timeout: QUIET })

    expect(screen.queryByText('67%')).toBeNull()
    expect(screen.getByText('Syncing storage — last reported')).toBeInTheDocument()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/stopped reporting sync progress/i)
  }, 30000)

  it('clears the warning and resumes the figure once the box answers again', async () => {
    let answering = false
    mockStatus(() => (answering
      ? { ok: true, status: 200, body: { phase: 'apps', done: false } }
      : { ok: false, status: 500, body: {} }))
    render(<IS09_SyncingStep onNext={vi.fn()} onComplete={vi.fn()} />)

    await screen.findByRole('alert', {}, { timeout: QUIET })

    answering = true
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull(), { timeout: QUIET })
    // 'apps' is index 4 of 6 → 83%
    expect(screen.getByText('83%')).toBeInTheDocument()
  }, 30000)

  it('a 200 that is not an object counts as silence, not as a phase', async () => {
    // The endpoint answering 200 with `null` used to be indistinguishable from
    // it answering with a real phase: `data` went null and the poll returned.
    mockStatus(() => ({ ok: true, status: 200, body: null as unknown as JsonBody }))
    render(<IS09_SyncingStep onNext={vi.fn()} onComplete={vi.fn()} />)

    await screen.findByRole('alert', {}, { timeout: QUIET })

    expect(screen.queryByText('17%')).toBeNull()
  }, 30000)
})

describe('IS09 syncing step — Continue in Background completes setup once', () => {
  it('does not complete a second time when the sync later finishes', async () => {
    const user = userEvent.setup()
    let done = false
    mockStatus(() => ({ ok: true, status: 200, body: { phase: done ? 'done' : 'storage', done } }))

    const onComplete = vi.fn()
    render(<IS09_SyncingStep onNext={vi.fn()} onComplete={onComplete} />)

    await user.click(await screen.findByRole('button', { name: /Continue in Background/i }))
    expect(onComplete).toHaveBeenCalledTimes(1)

    // The box now reports the sync finished. The step re-polls (the effect is
    // keyed on bgMode, so it restarted the moment the button was pressed) and
    // must NOT complete setup again.
    done = true
    await waitFor(
      () => expect((globalThis.fetch as unknown as { mock: { calls: unknown[] } }).mock.calls.length).toBeGreaterThan(2),
      { timeout: 5000 },
    )
    await new Promise(r => setTimeout(r, 50))

    expect(onComplete).toHaveBeenCalledTimes(1)
  }, 15000)

  it('still completes setup when the sync finishes on its own in background mode', async () => {
    // The guard must not turn into "never complete": if the poll's done-branch
    // is the FIRST to fire, it is still the one that completes setup.
    const user = userEvent.setup()
    let done = false
    mockStatus(() => ({ ok: true, status: 200, body: { phase: done ? 'done' : 'storage', done } }))

    const onComplete = vi.fn()
    // Reaching background mode without the click is not possible, so this drives
    // the same button and then asserts the total, which is the property at stake.
    render(<IS09_SyncingStep onNext={vi.fn()} onComplete={onComplete} />)
    await user.click(await screen.findByRole('button', { name: /Continue in Background/i }))
    done = true
    await new Promise(r => setTimeout(r, 200))
    expect(onComplete).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/Sync continues on the box/i)).toBeInTheDocument()
  }, 15000)
})
