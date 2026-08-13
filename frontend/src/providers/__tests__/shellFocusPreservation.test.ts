// shellFocusPreservation.test.ts — proves RESTORE_STATE's `fromMirror` flag
// (ShellProvider.tsx) stops a mirrored sync from yanking THIS instance's
// keyboard target to whatever the other instance last clicked.
//
// `activeWindow` is the field shell/useWindowShortcuts.ts reads to decide
// which window a keystroke acts on. Before this fix, EVERY RESTORE_STATE —
// mirrored or not — adopted `saved.activeWindow` verbatim, so a follower's
// own locally-focused window would be silently overwritten the next time the
// writer's desktop changed for ANY reason (moving an unrelated window,
// opening a new one, resizing) — a debounced event that fires roughly every
// 500ms while anything is happening on the writer's screen. See the
// RESTORE_STATE reducer case in ShellProvider.tsx and roadmap/SCREENS.md,
// "Input focus across instances", for the full argument.
//
// Each test below models TWO instances' worth of state explicitly — "the
// writer's snapshot" (what crosses the wire) and "this follower's own prior
// state" (what's already in its reducer before the mirrored dispatch lands)
// — same shape as shellStateGeometry.test.ts's cross-viewport pairs, and for
// the same reason: a test with only one instance's state can't tell a fix
// that actually preserves the local pick apart from one that just happens to
// agree with it.

import { describe, expect, it } from 'vitest'
import { shellReducer, type ShellWindow } from '../ShellProvider'

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
  return { id, appId: 'terminal', title: `Window ${id}`, position: { x: 100, y: 100 }, size: { width: 720, height: 500 }, minimized: false, ...over }
}

describe('RESTORE_STATE — fromMirror preserves this instance\'s own active window', () => {
  it('a mirrored sync does not steal the follower\'s locally-focused window', () => {
    // Follower B has windows 1 and 2 open, and its OWN user just clicked
    // window 2 (locally — FOCUS_WINDOW dispatches unconditionally, on every
    // instance, regardless of writer/follower role).
    const followerState = baseState([win(1), win(2)], 2)

    // The writer (instance A) publishes a snapshot where window 1 is active —
    // A's own user clicked window 1, on A's own screen, unrelated to B.
    const writerSnapshot = baseState([win(1), win(2)], 1)

    const result = shellReducer(followerState, {
      type: 'RESTORE_STATE',
      saved: { desktops: writerSnapshot.desktops, activeDesktop: 'desktop-1' },
      fromMirror: true,
    })

    // B's own selection survives — a keystroke on B still hits window 2, not
    // whatever A last clicked.
    expect(result.desktops['desktop-1'].activeWindow).toBe(2)
  })

  it('a LOCAL restore (no fromMirror) still adopts the saved value verbatim — this tab\'s own prior session, not a peer\'s', () => {
    const priorLocalState = baseState([win(1), win(2)], 2)
    const savedFromDisk = baseState([win(1), win(2)], 1)

    const result = shellReducer(priorLocalState, {
      type: 'RESTORE_STATE',
      saved: { desktops: savedFromDisk.desktops, activeDesktop: 'desktop-1' },
      // fromMirror omitted — this is the localStorage-on-mount path.
    })

    expect(result.desktops['desktop-1'].activeWindow).toBe(1)
  })

  it('falls back to the writer\'s pick the first time a follower observes a desktop (no local pick yet)', () => {
    const freshFollowerState = baseState([], null) // never had anything open
    const writerSnapshot = baseState([win(1)], 1)

    const result = shellReducer(freshFollowerState, {
      type: 'RESTORE_STATE',
      saved: { desktops: writerSnapshot.desktops, activeDesktop: 'desktop-1' },
      fromMirror: true,
    })

    expect(result.desktops['desktop-1'].activeWindow).toBe(1)
  })

  it('falls back to the writer\'s pick once the follower\'s own locally-active window has been closed out from under it', () => {
    // Follower had window 2 active, but the incoming snapshot no longer has
    // window 2 at all (closed on the writer) — nothing local left to keep.
    const followerState = baseState([win(1), win(2)], 2)
    const writerSnapshot = baseState([win(1)], 1)

    const result = shellReducer(followerState, {
      type: 'RESTORE_STATE',
      saved: { desktops: writerSnapshot.desktops, activeDesktop: 'desktop-1' },
      fromMirror: true,
    })

    expect(result.desktops['desktop-1'].activeWindow).toBe(1)
  })

  it('the bug this file exists to catch: WITHOUT the preservation logic, a mirrored sync clobbers the local pick', () => {
    // Sanity check on the test's own ability to fail — same assertion as the
    // first test, but omitting fromMirror reproduces the pre-fix behaviour
    // (every RESTORE_STATE adopts saved.activeWindow unconditionally), so
    // this MUST show the writer's pick (1), not the follower's (2).
    const followerState = baseState([win(1), win(2)], 2)
    const writerSnapshot = baseState([win(1), win(2)], 1)

    const result = shellReducer(followerState, {
      type: 'RESTORE_STATE',
      saved: { desktops: writerSnapshot.desktops, activeDesktop: 'desktop-1' },
      // fromMirror deliberately omitted to demonstrate the old, unsafe path.
    })

    expect(result.desktops['desktop-1'].activeWindow).toBe(1)
  })
})
