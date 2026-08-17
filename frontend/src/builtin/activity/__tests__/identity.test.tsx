import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, act } from '@testing-library/react'

/**
 * Can this app be made to end a process the user did not choose?
 *
 * # Why the server's guard is not enough on its own
 *
 * The backend refuses any kill whose (pid, starttime) pair no longer holds, and
 * that guard is real — it is mutation-tested in internal/proctl. But it can only
 * catch a client that sends a STALE pair. It cannot catch a client that sends a
 * FRESH pair for the wrong process, because such a request is, on the wire,
 * indistinguishable from a correct one.
 *
 * That is precisely what a pid-keyed selection produces. The list is re-polled
 * every three seconds and the selection is re-resolved against the newest one,
 * so when a selected process exits and the kernel hands its number to something
 * else, the highlighted row silently becomes a different program — same pid,
 * same position, same highlight. Confirming then reads pid AND starttime off
 * that new row, and the two agree. The server verifies them, finds nothing
 * wrong, and kills it.
 *
 * So the pairing has to happen at SELECTION time. These tests drive the real
 * component through a real recycle and assert that the request is never formed.
 *
 * # The control matters as much as the assertion
 *
 * "The Quit button is disabled" is also what a completely broken feature looks
 * like. Every negative case here is paired with a positive one proving the same
 * button is enabled, and the same request IS sent, when the identity holds.
 */

// The Activity Monitor asks who you are before it offers to end anything.
// Admin by default here so these tests stay about what they are testing; the
// non-admin affordance has its own file.
const mockProfile: Record<string, unknown> | null = { role: 'admin' }
vi.mock('../../../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: mockProfile }),
}))

vi.mock('../../../core/useTelemetry', () => ({
  // connected: true so the "telemetry is down" banner — itself a role=status
  // — is not rendered. These tests assert on the ACTION notice's role, and two
  // live regions would make that assertion match the wrong one.
  useTelemetry: () => ({ stats: null, connected: true }),
}))

// jsdom has no ResizeObserver and the graph cards construct one on mount. A
// no-op is the right stub here: these tests are about the process table, and a
// stub that fired callbacks would re-enter the resize path this component has
// already had one render-loop bug in.
class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= NoopResizeObserver as unknown as typeof ResizeObserver

import ActivityMonitor, { describeOutcome } from '../ActivityMonitor'
import { processKey } from '../api'

interface Row { pid: number; name: string; start?: number; user?: string; state?: string }

/** The process list the next poll will return. Mutated between polls. */
let procRows: Row[] = []
/** Every POST the component made, in order. */
let posts: { url: string; body: Record<string, unknown> }[] = []
/** What the next signal POST should answer with. */
let signalReply: { status: number; body: unknown } = { status: 200, body: {} }
/** When true, the process listing fails — the refresh cannot correct anything. */
let procFeedDown = false

function installFetch() {
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    if (init?.method === 'POST') {
      posts.push({ url: u, body: JSON.parse(String(init.body)) })
      return Promise.resolve({
        ok: signalReply.status < 400,
        status: signalReply.status,
        json: () => Promise.resolve(signalReply.body),
      })
    }
    if (procFeedDown && u.includes('/api/system/processes')) {
      return Promise.resolve({
        ok: false, status: 503,
        json: () => Promise.resolve({ error: 'cannot read /proc' }),
      })
    }
    const body = u.includes('/api/system/processes') ? procRows
      : u.includes('/api/proc/apps') ? { apps: [] }
        : []
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
  }))
}

/**
 * Runs the 3s poll interval and lets every resulting promise settle.
 *
 * act() is not decoration here. The poll resolves a fetch and calls setState
 * from outside React's event system; without act the state lands but the DOM
 * is never re-rendered, so the table keeps showing the PREVIOUS poll. Every
 * assertion about "what the user sees after a recycle" would then be reading
 * pre-recycle markup and passing for the wrong reason.
 */
async function nextPoll() {
  await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
}

/** Settles in-flight promises (a signal POST and the refresh that follows). */
async function settle() {
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
}

/** The action bar's Quit button (not the dialog's, which has the same word). */
function quitButton(): HTMLButtonElement {
  return screen.getAllByRole('button', { name: 'Quit' })[0] as HTMLButtonElement
}

function row(name: string) {
  return screen.getByText(name).closest('[role="row"]') as HTMLElement
}

beforeEach(async () => {
  posts = []
  procFeedDown = false
  signalReply = { status: 200, body: { outcome: 'terminated', signals: ['SIGTERM'], elapsed_ms: 40 } }
  procRows = [
    { pid: 4242, name: 'editor', start: 111, user: 'nobody', state: 'S' },
    { pid: 9001, name: 'daemon', start: 222, user: 'root', state: 'S' },
  ]
  vi.useFakeTimers()
  installFetch()
  render(<ActivityMonitor />)
  // Let the mount poll land so the table has rows.
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('a recycled pid under a live selection', () => {
  it('CONTROL: while the identity holds, the selection survives a poll and Quit is enabled', async () => {
    fireEvent.click(row('editor'))
    expect(quitButton().disabled).toBe(false)

    // Same pid, same starttime — the same process, merely re-reported.
    procRows = [
      { pid: 4242, name: 'editor', start: 111, user: 'nobody', state: 'S' },
      { pid: 9001, name: 'daemon', start: 222, user: 'root', state: 'S' },
    ]
    await nextPoll()

    expect(quitButton().disabled).toBe(false)
    expect(screen.getByText(/editor \(4242\)/)).toBeTruthy()
  })

  it('drops the selection when the pid is handed to a different process', async () => {
    fireEvent.click(row('editor'))
    expect(quitButton().disabled).toBe(false)

    // `editor` exited; the kernel gave 4242 to sshd. Same number, new process,
    // so a NEW starttime — which is the only thing that distinguishes them.
    procRows = [
      { pid: 4242, name: 'sshd', start: 987654, user: 'root', state: 'S' },
      { pid: 9001, name: 'daemon', start: 222, user: 'root', state: 'S' },
    ]
    await nextPoll()

    expect(quitButton().disabled).toBe(true)
    expect(screen.getByText('Select a process to end it')).toBeTruthy()
  })

  it('sends NO signal for the stranger that inherited the number', async () => {
    fireEvent.click(row('editor'))
    procRows = [{ pid: 4242, name: 'sshd', start: 987654, user: 'root', state: 'S' }]
    await nextPoll()

    // The button is disabled, but a disabled button is a UI fact and this is a
    // security property — fire the click anyway and prove nothing goes out.
    fireEvent.click(quitButton())
    await settle()

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts.filter(p => p.url.includes('/signal'))).toHaveLength(0)
  })

  it('CONTROL: the kill it does send carries the starttime of the row the user picked', async () => {
    fireEvent.click(row('editor'))
    fireEvent.click(quitButton())
    // Confirm in the dialog.
    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    const sent = posts.filter(p => p.url.includes('/signal'))
    expect(sent).toHaveLength(1)
    expect(sent[0].body).toEqual({ pid: 4242, start: 111, mode: 'quit' })
  })
})

describe('the 409 the server sends when a pid changed hands', () => {
  it('does not leave the stale selection armed for a second, successful click', async () => {
    fireEvent.click(row('editor'))
    signalReply = {
      status: 409,
      body: { error: 'that pid now belongs to a different process; refresh the list', code: 'identity_mismatch' },
    }
    // The list now reports the stranger, exactly as it would after the refresh
    // the server's message asks for.
    procRows = [{ pid: 4242, name: 'sshd', start: 987654, user: 'root', state: 'S' }]

    fireEvent.click(quitButton())
    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    expect(screen.getByRole('alert').textContent).toContain('refresh the list')
    // The selection must be gone. If it survived, it would re-resolve onto the
    // stranger and the next click would echo the stranger's own starttime —
    // which the server WOULD accept.
    expect(quitButton().disabled).toBe(true)
    expect(posts.filter(p => p.url.includes('/signal'))).toHaveLength(1)
  })

  /**
   * The refresh is not what makes this safe.
   *
   * When the listing comes back with the stranger in it, the identity-keyed
   * lookup drops the selection on its own — so a test where the refresh
   * succeeds cannot tell whether the explicit clear does anything.
   *
   * Here the refresh FAILS, which is entirely reachable: the box that just
   * changed its mind about a pid is a box under load, and the process feed is
   * the first thing to time out. `processes` then keeps the pre-kill list, the
   * selection still resolves against it, and the only thing standing between
   * the user and a second confirmed click is the clear in the failure branch.
   */
  it('clears the selection even when the refresh that would have corrected it fails', async () => {
    fireEvent.click(row('editor'))
    signalReply = {
      status: 409,
      body: { error: 'that pid now belongs to a different process; refresh the list', code: 'identity_mismatch' },
    }
    procFeedDown = true

    fireEvent.click(quitButton())
    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    expect(quitButton().disabled).toBe(true)
  })

  /**
   * CONTROL for the test above: an ordinary failure must NOT deselect.
   *
   * Without this, "clears the selection" is satisfied by clearing it after
   * every failed action, which would make the app lose the user's place on a
   * transient 503 they should simply be able to retry.
   */
  it('CONTROL: a failure that is not an identity mismatch keeps the selection', async () => {
    fireEvent.click(row('editor'))
    signalReply = { status: 503, body: { error: 'exec disabled by administrator' } }
    procFeedDown = true

    fireEvent.click(quitButton())
    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    expect(quitButton().disabled).toBe(false)
  })
})

describe('processKey', () => {
  it('separates two processes that share a pid across a recycle', () => {
    expect(processKey({ pid: 4242, start: 111 })).not.toBe(processKey({ pid: 4242, start: 987654 }))
  })

  it('is stable for the same process', () => {
    expect(processKey({ pid: 4242, start: 111 })).toBe(processKey({ pid: 4242, start: 111 }))
  })

  it('does not let a row with no starttime collide with one that has a real one', () => {
    expect(processKey({ pid: 7 })).not.toBe(processKey({ pid: 7, start: 0 }))
  })
})

describe('what the user is told about the escalation', () => {
  it('says the quit was refused and the process was force-ended', () => {
    const r = describeOutcome('notes (4242)', {
      outcome: 'killed', signals: ['SIGTERM', 'SIGKILL'], elapsed_ms: 5040,
    })
    expect(r.text).toContain('ignored the request to quit')
    expect(r.text).toContain('SIGTERM')
    expect(r.text).toContain('SIGKILL')
    expect(r.text).toContain('5.0s')
    expect(r.text).toMatch(/unsaved work/i)
    // Not `ok`: the user asked for a polite exit and did not get one.
    expect(r.tone).not.toBe('ok')
  })

  it('does not claim an escalation when SIGTERM alone was enough', () => {
    const r = describeOutcome('notes (4242)', {
      outcome: 'terminated', signals: ['SIGTERM'], elapsed_ms: 120,
    })
    expect(r.text).not.toContain('SIGKILL')
    expect(r.text).toMatch(/chance to save/i)
    expect(r.tone).toBe('ok')
  })

  it('distinguishes a force quit the user asked for from an escalation they did not', () => {
    const forced = describeOutcome('game (7)', { outcome: 'killed', signals: ['SIGKILL'] })
    const escalated = describeOutcome('game (7)', { outcome: 'killed', signals: ['SIGTERM', 'SIGKILL'] })
    expect(forced.text).not.toContain('ignored')
    expect(escalated.text).toContain('ignored')
    expect(forced.tone).not.toBe(escalated.tone)
  })

  it('never reports a survived process as success', () => {
    const r = describeOutcome('backup (99)', {
      outcome: 'survived', signals: ['SIGTERM', 'SIGKILL'], state: 'D',
    })
    expect(r.tone).toBe('bad')
    expect(r.text).toMatch(/could not be ended/i)
    expect(r.text).toContain('D')
  })

  it('says nothing was needed when the process had already exited', () => {
    const r = describeOutcome('gone (5)', { outcome: 'already_gone', signals: [] })
    expect(r.text).toMatch(/already exited/i)
    expect(r.tone).toBe('ok')
  })
})

describe('the notice for a costly or failed action interrupts', () => {
  it('is an alert, not a polite status, when work was destroyed', async () => {
    fireEvent.click(row('editor'))
    signalReply = {
      status: 200,
      body: { outcome: 'killed', signals: ['SIGTERM', 'SIGKILL'], elapsed_ms: 5000 },
    }
    fireEvent.click(quitButton())
    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    expect(screen.getByRole('alert').textContent).toContain('force-ended')
  })

  it('CONTROL: a clean exit is a status, not an alert', async () => {
    fireEvent.click(row('editor'))
    signalReply = { status: 200, body: { outcome: 'terminated', signals: ['SIGTERM'], elapsed_ms: 30 } }
    fireEvent.click(quitButton())
    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByRole('status').textContent).toMatch(/exited/i)
  })
})
