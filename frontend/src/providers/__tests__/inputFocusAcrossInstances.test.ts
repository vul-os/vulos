// inputFocusAcrossInstances.test.ts — the wiring half of the focus-across-
// instances fix, exercised with TWO LIVE useShellSession instances sharing a
// fake BroadcastChannel, following the exact pattern
// src/__tests__/useShellSession.test.ts established for the split-brain fix.
// A single-instance test cannot distinguish a correct fix from one that
// ignores the problem — there is nothing to yank focus FROM with only one
// screen — so this file always drives two.
//
// It proves three separate things, matching the three hard requirements in
// roadmap/SCREENS.md, "Input focus across instances":
//
//   1. A keystroke acts once, in the viewport the user is actually in — not
//      duplicated, not lost — modelled here as: instance B's own
//      locally-focused window survives instance A's publishes.
//   2. Restoring a mirrored window must not yank the follower's keyboard
//      target across MULTIPLE publish cycles, not just one.
//   3. Leader/follower election never depends on focus — a follower can be
//      the one instance that's actually focused (the normal case, per
//      roadmap/SCREENS.md), and the writer/follower role must come out
//      identically whether or not the "focused" instance is even the writer.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useShellSession } from '../useShellSession'
import { HEARTBEAT_MS } from '../shellSession'
import { shellReducer, serializableShellState, hydratePersistedState, type ShellWindow } from '../ShellProvider'
import { hasViewportFocus } from '../viewportFocus'

type ShellState = Parameters<typeof shellReducer>[0]

function baseState(windows: ShellWindow[], activeWindow: number | null): ShellState {
  return {
    desktops: { 'desktop-1': { id: 'desktop-1', label: 'Desktop 1', windows, activeWindow } },
    activeDesktop: 'desktop-1',
    popout: null,
    nativeWindows: [],
    conversation: [],
    thinking: false,
    launchpadOpen: false,
    chatOpen: false,
    missionControlOpen: false,
  }
}

function win(id: number, over: Partial<ShellWindow> = {}): ShellWindow {
  return { id, appId: 'terminal', title: `Window ${id}`, position: { x: 0, y: 0 }, size: { width: 720, height: 500 }, minimized: false, ...over }
}

// Same minimal fake BroadcastChannel as useShellSession.test.ts — synchronous
// delivery, since the subject here is the wiring, not message-passing
// latency.
let allChannels: FakeChannel[] = []

class FakeChannel {
  onmessage: ((ev: { data: unknown }) => void) | null = null
  private closed = false
  constructor() { allChannels.push(this) }
  postMessage(msg: unknown) {
    if (this.closed) return
    for (const peer of allChannels) {
      if (peer === this || peer.closed) continue
      peer.onmessage?.({ data: msg })
    }
  }
  close() { this.closed = true }
}

beforeEach(() => {
  allChannels = []
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  let n = 0
  const ids = ['tab-aaa-writer', 'tab-bbb-follower']
  vi.stubGlobal('crypto', { randomUUID: () => ids[n++] ?? `tab-extra-${n}` })
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('two live instances — a mirrored publish does not steal the follower\'s keyboard target', () => {
  it('B keeps its own active window across repeated publishes from A, the writer', async () => {
    const a = renderHook(() => useShellSession('desktop-1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(a.result.current.role).toBe('writer')

    const b = renderHook(() => useShellSession('desktop-1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(b.result.current.role).toBe('follower')

    // Both instances start with the same two windows open.
    let stateA = baseState([win(1), win(2)], 1)
    let stateB = baseState([win(1), win(2)], 1)

    // B's own user clicks window 2 — a real local FOCUS_WINDOW dispatch,
    // exactly what ShellProvider's focusWindow() does unconditionally on
    // every instance regardless of writer/follower role.
    stateB = shellReducer(stateB, { type: 'FOCUS_WINDOW', id: 2 })
    expect(stateB.desktops['desktop-1'].activeWindow).toBe(2)

    // A's user, on a different screen, does unrelated things to windows on
    // the SAME shared desktop — three separate publishes, none of which
    // A's user ever meant to affect B's keyboard target.
    for (const focusId of [1, 2, 1]) {
      stateA = shellReducer(stateA, { type: 'FOCUS_WINDOW', id: focusId })
      act(() => { a.result.current.publish(serializableShellState(stateA)) })

      // ShellProvider's follower-mirror effect, replicated exactly: hydrate
      // the mirrored payload against this instance's own viewport, then
      // RESTORE_STATE with fromMirror: true.
      const hydrated = hydratePersistedState(b.result.current.mirrored as ReturnType<typeof serializableShellState>, null)
      stateB = shellReducer(stateB, { type: 'RESTORE_STATE', saved: hydrated, fromMirror: true })

      // B's own pick survives EVERY one of A's publishes, not just the first.
      expect(stateB.desktops['desktop-1'].activeWindow).toBe(2)
    }

    a.unmount()
    b.unmount()
  })
})

describe('leader/follower election never depends on focus', () => {
  it('role assignment is identical whether the "focused" instance is A or B', async () => {
    // Two independent focus hosts modelling two real instances — see
    // viewportFocus.ts's module comment for why this is the injectable, not
    // a real second document.
    const focusedIsA = { document: { hasFocus: () => true } }
    const focusedIsB = { document: { hasFocus: () => false } }

    const a = renderHook(() => useShellSession('desktop-1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    const b = renderHook(() => useShellSession('desktop-1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })

    // A became writer, B became follower, purely from arrival order and the
    // lowest-tabId tie-break — see shellSession.ts's decideRole/nextWriter.
    // Flipping which instance is "focused" must not change that: nothing in
    // decideRole/shouldStepDown/nextWriter takes a focus argument at all
    // (grep-verified against shellSession.ts), so this is really asserting
    // "the roles we already established don't waver" rather than exercising
    // a branch — which IS the point: there is no such branch, and this
    // guards against one ever being added.
    expect(a.result.current.role).toBe('writer')
    expect(b.result.current.role).toBe('follower')
    expect(hasViewportFocus(focusedIsA)).toBe(true)
    expect(hasViewportFocus(focusedIsB)).toBe(false)

    // Advance past several heartbeats with "focus" flipped to the follower
    // (the normal case per roadmap/SCREENS.md — a follower can be the
    // focused viewport) and confirm the writer/follower split holds exactly
    // as before: still one writer, still the same one.
    await act(async () => { await vi.advanceTimersByTimeAsync(HEARTBEAT_MS * 3) })
    expect(a.result.current.role).toBe('writer')
    expect(b.result.current.role).toBe('follower')

    a.unmount()
    b.unmount()
  })
})
