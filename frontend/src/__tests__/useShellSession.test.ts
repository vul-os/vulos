// useShellSession.test.ts — the wiring half of the split-brain fix.
//
// shellSession.test.ts proves shouldStepDown is correct in isolation. That is
// necessary but not sufficient: the actual hazard lives in useShellSession.ts
// calling it at the right moments, with the right peer set, and this file
// exists to prove the WIRING, not just the pure function.
//
// The scenario under test is specifically the one a tab closing can never
// produce: a writer goes silent (no `bye`, no unmount, no cleanup effect
// runs — its React tree is never torn down) for long enough that a follower
// promotes itself, and only THEN does the original writer's next heartbeat
// arrive, still claiming 'writer'. A real cause would be a display dropping
// out from under a browser instance that keeps running — see shellSession.ts
// for why that is plausible and not the same event as a tab closing.
//
// To simulate "goes silent without cleanup" faithfully, this test never
// unmounts or calls bye() for the writer tab during the scenario — it makes
// the fake BroadcastChannel stop delivering that tab's traffic for a window,
// which is the effect a throttled/backgrounded tab has on its peers without
// requiring any teardown path to run. Calling the real cleanup instead would
// prove nothing about an unplugged monitor, per the task this file exists for.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useShellSession } from '../providers/useShellSession'
import { HEARTBEAT_MS, WRITER_TIMEOUT_MS } from '../providers/shellSession'

// ─── A minimal, fully controllable stand-in for BroadcastChannel ──────────
//
// Delivers synchronously (unlike the real thing, which is async) — the
// subject under test is the election/step-down logic and its wiring, not
// message-passing latency, and synchronous delivery keeps this file free of
// fake-timer/microtask interaction bugs that add risk without adding
// coverage of anything this task is about.
//
// `quarantine`/`release` model a tab going silent: while quarantined, a
// channel's outgoing messages are dropped before reaching any peer AND
// incoming messages addressed to it are dropped too — approximating "this
// tab's event loop is not running right now" without literally being able to
// suspend one renderHook's timers independently of another's in a
// single-threaded test process.
let allChannels: FakeChannel[] = []
let quarantined: Set<FakeChannel>

class FakeChannel {
  onmessage: ((ev: { data: unknown }) => void) | null = null
  private closed = false
  constructor() {
    allChannels.push(this)
  }
  postMessage(msg: unknown) {
    if (this.closed || quarantined.has(this)) return
    for (const peer of allChannels) {
      if (peer === this || peer.closed || quarantined.has(peer)) continue
      peer.onmessage?.({ data: msg })
    }
  }
  close() {
    this.closed = true
  }
}

beforeEach(() => {
  allChannels = []
  quarantined = new Set()
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  // Deterministic tab ids so the lowest-tabId tie-break is under the test's
  // control rather than depending on crypto.randomUUID's actual output. The
  // writer tab intentionally gets the id that must NOT survive a conflict —
  // see the test below for why.
  let n = 0
  const ids = ['tab-zzz-writer', 'tab-aaa-follower']
  vi.stubGlobal('crypto', { randomUUID: () => ids[n++] ?? `tab-extra-${n}` })
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useShellSession — surviving a writer that goes silent without cleanup', () => {
  it('promotes a follower, then demotes the original writer when it resurfaces still claiming the role', async () => {
    const a = renderHook(() => useShellSession('desktop-1')) // becomes writer: tab-zzz-writer
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300) // past the 250ms settle
    })
    expect(a.result.current.role).toBe('writer')
    expect(a.result.current.tabId).toBe('tab-zzz-writer')

    const b = renderHook(() => useShellSession('desktop-1')) // becomes follower: tab-aaa-follower
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300)
    })
    expect(b.result.current.role).toBe('follower')

    // The writer goes silent WITHOUT unmounting, WITHOUT sending `bye`, and
    // WITHOUT this test calling any cleanup on its behalf — this is the
    // "display disappeared, process didn't" case, not the "tab closed" case.
    const writerChannel = allChannels[0]
    quarantined.add(writerChannel)

    // Past WRITER_TIMEOUT_MS plus at least one of B's own heartbeat ticks, so
    // B's periodic promotion check (the follower branch in useShellSession's
    // `beat` interval) has had a chance to run and see the writer as stale.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITER_TIMEOUT_MS + HEARTBEAT_MS + 100)
    })
    expect(b.result.current.role).toBe('writer')
    expect(a.result.current.role).toBe('writer') // A never heard about any of this — still believes it leads.

    // The display comes back (or the throttled tab's event loop simply
    // resumes) and its next heartbeat, still announcing 'writer', reaches B.
    quarantined.delete(writerChannel)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(HEARTBEAT_MS + 100)
    })

    // Split-brain resolved deterministically: B ('tab-aaa-follower') sorts
    // before A ('tab-zzz-writer'), so A is the one that steps down. Exactly
    // one of the two is 'writer' — never both, never neither.
    expect(a.result.current.role).toBe('follower')
    expect(b.result.current.role).toBe('writer')

    a.unmount()
    b.unmount()
  })

  it('never leaves both tabs claiming writer at once once heartbeats have crossed', async () => {
    const a = renderHook(() => useShellSession('desktop-1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    const b = renderHook(() => useShellSession('desktop-1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    const writerChannel = allChannels[0]
    quarantined.add(writerChannel)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITER_TIMEOUT_MS + HEARTBEAT_MS + 100)
    })
    quarantined.delete(writerChannel)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(HEARTBEAT_MS + 100)
    })

    const roles = [a.result.current.role, b.result.current.role]
    expect(roles.filter(r => r === 'writer')).toHaveLength(1)

    a.unmount()
    b.unmount()
  })
})
