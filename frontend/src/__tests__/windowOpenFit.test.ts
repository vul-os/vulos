// windowOpenFit.test.ts — a freshly opened window fits the screen it opens on,
// and NOTHING ELSE about window geometry is clamped.
//
// The defect: ShellProvider's OPEN_WINDOW placed window N at
// x = 60 + (N % 6) * 32 with a flat 720x500 size and consulted no viewport, so
// on a 768px-wide screen (the narrowest the desktop canvas is ever mounted at —
// shell/viewportRule.ts) the first window's right edge landed at 780px and the
// sixth at 940px. e2e/windows-open-geometry.e2e.ts measures that in a real
// browser; this file pins the arithmetic and, more importantly, pins the
// BOUNDARY of the clamp — which paths must NOT be clamped.
//
// The boundary is the whole design decision. Opening geometry is invented by
// the shell; a drag, a resize, a tile, and a restore are the user's own
// arrangement. Clamping the second kind would either cage a window someone
// deliberately dragged off-screen, or — at hydrate — write a small screen's
// limits back into persisted state, because the writer re-serializes whatever
// is in state as a fraction of its own viewport.

import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  openWindowGeometry, DEFAULT_WINDOW_SIZE, WINDOW_EDGE_MARGIN,
  OPEN_CASCADE_ORIGIN, OPEN_CASCADE_STEP, OPEN_CASCADE_WRAP,
  MENU_BAR_H, DOCK_H,
} from '../shell/windowTiling'
import {
  shellReducer, serializableShellState, hydratePersistedState,
  type ShellWindow,
} from '../providers/ShellProvider'
import { readViewportSize, viewportPx } from '../providers/screenScale'

type ShellState = Parameters<typeof shellReducer>[0]

function baseState(windows: ShellWindow[] = []): ShellState {
  return {
    desktops: { 'desktop-1': { id: 'desktop-1', label: 'Desktop 1', windows, activeWindow: windows[0]?.id ?? null } },
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

function extent(w: number, h: number) {
  return { widthPx: w, heightPx: h }
}

/** How far a geometry pokes outside a viewport, per side. Zero on all four is
 *  the only thing "opens fully on screen" can mean. */
function overflow(g: ReturnType<typeof openWindowGeometry>, vw: number, vh: number) {
  return {
    left: Math.max(0, -g.position.x),
    top: Math.max(0, -g.position.y),
    right: Math.max(0, g.position.x + g.size.width - vw),
    bottom: Math.max(0, g.position.y + g.size.height - vh),
  }
}

const NO_OVERFLOW = { left: 0, top: 0, right: 0, bottom: 0 }

// Every width the desktop canvas can be mounted at, from its floor upward.
// 768 first, because it is the width that failed and the width a regression
// would fail at again.
const CANVAS_WIDTHS: Array<[number, number]> = [[768, 1024], [834, 1194], [1024, 768], [1280, 800], [1440, 900], [2560, 1440]]

describe('openWindowGeometry — a window opens fully on screen', () => {
  it.each(CANVAS_WIDTHS)('fits every window of the cascade at %ix%i', (vw, vh) => {
    for (let i = 0; i < OPEN_CASCADE_WRAP; i++) {
      const g = openWindowGeometry(i, DEFAULT_WINDOW_SIZE, extent(vw, vh))
      expect(overflow(g, vw, vh), `window ${i + 1} at ${vw}x${vh}`).toEqual(NO_OVERFLOW)
    }
  })

  it('clears the menu bar at the top and the dock at the bottom, which the raw viewport test cannot see', () => {
    // 1024x768 is the case that proves this is a separate assertion: at that
    // height the sixth window's ideal y of 210 puts its bottom at 710 — inside
    // the viewport, and 10px underneath a dock that takes pointer events.
    for (let i = 0; i < OPEN_CASCADE_WRAP; i++) {
      const g = openWindowGeometry(i, DEFAULT_WINDOW_SIZE, extent(1024, 768))
      expect(g.position.y, `window ${i + 1} top`).toBeGreaterThanOrEqual(MENU_BAR_H)
      expect(g.position.y + g.size.height, `window ${i + 1} bottom`).toBeLessThanOrEqual(768 - DOCK_H)
    }
    // …and it really is the sixth window that gets moved, not a no-op loop.
    expect(openWindowGeometry(5, DEFAULT_WINDOW_SIZE, extent(1024, 768)).position.y).toBe(768 - DOCK_H - 500)
  })

  it('moves the window rather than shrinking it, when moving is enough', () => {
    // 720 wide still fits inside 768 - 2*8; only the x offset has to give.
    const g = openWindowGeometry(0, DEFAULT_WINDOW_SIZE, extent(768, 1024))
    expect(g.size).toEqual({ width: 720, height: 500 })
    expect(g.position.x).toBe(768 - WINDOW_EDGE_MARGIN - 720)
  })

  it('shrinks the window only when no position could make it fit', () => {
    // A window larger than the screen in both axes: 1200 > 768 - 2*8, and
    // 1200 > 1024 - 32 - 68.
    const g = openWindowGeometry(0, { width: 1200, height: 1200 }, extent(768, 1024))
    expect(g.size.width).toBe(768 - 2 * WINDOW_EDGE_MARGIN)
    expect(g.size.height).toBe(1024 - MENU_BAR_H - DOCK_H)
    expect(overflow(g, 768, 1024)).toEqual(NO_OVERFLOW)
  })

  it('leaves the historical cascade EXACTLY as it was wherever it already fitted', () => {
    // The clamp must not become a redesign of window placement on the screens
    // that were never broken. 1440x900 has room for the whole cascade.
    for (let i = 0; i < OPEN_CASCADE_WRAP; i++) {
      const g = openWindowGeometry(i, DEFAULT_WINDOW_SIZE, extent(1440, 900))
      expect(g.position).toEqual({
        x: OPEN_CASCADE_ORIGIN.x + i * OPEN_CASCADE_STEP,
        y: OPEN_CASCADE_ORIGIN.y + i * OPEN_CASCADE_STEP,
      })
      expect(g.size).toEqual(DEFAULT_WINDOW_SIZE)
    }
  })

  it('keeps a stack of windows readable as a stack even where the horizontal cascade collapses', () => {
    // At 768 there is only 40px of horizontal travel, so every window shares an
    // x. If the vertical cascade collapsed too, six windows would be six
    // pixel-identical rectangles and the desktop would look like one window.
    const ys = new Set<number>()
    const xs = new Set<number>()
    for (let i = 0; i < OPEN_CASCADE_WRAP; i++) {
      const g = openWindowGeometry(i, DEFAULT_WINDOW_SIZE, extent(768, 1024))
      ys.add(g.position.y)
      xs.add(g.position.x)
    }
    expect(xs.size).toBe(1)                    // horizontal cascade has nowhere to go…
    expect(ys.size).toBe(OPEN_CASCADE_WRAP)    // …vertical cascade is intact
  })

  it('wraps the cascade every OPEN_CASCADE_WRAP windows', () => {
    const a = openWindowGeometry(0, DEFAULT_WINDOW_SIZE, extent(1440, 900))
    const b = openWindowGeometry(OPEN_CASCADE_WRAP, DEFAULT_WINDOW_SIZE, extent(1440, 900))
    expect(b).toEqual(a)
    // …and does not wrap early, which an off-by-one in the modulus would do.
    expect(openWindowGeometry(OPEN_CASCADE_WRAP - 1, DEFAULT_WINDOW_SIZE, extent(1440, 900))).not.toEqual(a)
  })

  it('reproduces the pre-clamp geometry exactly when the viewport cannot be read at all', () => {
    // SSR / a unit test with no `window`. Inventing an extent to clamp against
    // would be worse than not clamping: it would move windows on real screens
    // whose size this code never saw.
    for (let i = 0; i < OPEN_CASCADE_WRAP; i++) {
      const g = openWindowGeometry(i, DEFAULT_WINDOW_SIZE, null)
      expect(g.position).toEqual({ x: 60 + i * 32, y: 50 + i * 32 })
      expect(g.size).toEqual({ width: 720, height: 500 })
    }
  })
})

describe('OPEN_WINDOW — the reducer applies the fit against the live viewport', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('opens the first window fully inside a 768x1024 screen', () => {
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    const s = shellReducer(baseState(), { type: 'OPEN_WINDOW', appId: 'terminal', title: 'Terminal' })
    const w = s.desktops['desktop-1'].windows[0]
    expect(w.position.x + w.size.width).toBeLessThanOrEqual(768)
    // The pre-fix value, named so this cannot pass by coincidence.
    expect(w.position.x + w.size.width).not.toBe(780)
  })

  it('opens the SIXTH window fully inside a 768x1024 screen (the cascade accumulates)', () => {
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    let s = baseState()
    for (let i = 0; i < 6; i++) s = shellReducer(s, { type: 'OPEN_WINDOW', appId: `app-${i}` })
    const w = s.desktops['desktop-1'].windows[5]
    expect(w.position.x + w.size.width).toBeLessThanOrEqual(768)
    expect(w.position.y + w.size.height).toBeLessThanOrEqual(1024)
    expect(w.position.x + w.size.width).not.toBe(940) // pre-fix
  })

  it('honours Settings\' larger explicit size where it fits, and fits it where it does not', () => {
    vi.stubGlobal('innerWidth', 1440)
    vi.stubGlobal('innerHeight', 900)
    const big = shellReducer(baseState(), { type: 'OPEN_WINDOW', appId: 'persona', size: { width: 860, height: 620 } })
    expect(big.desktops['desktop-1'].windows[0].size).toEqual({ width: 860, height: 620 })

    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    const small = shellReducer(baseState(), { type: 'OPEN_WINDOW', appId: 'persona', size: { width: 860, height: 620 } })
    const w = small.desktops['desktop-1'].windows[0]
    expect(w.size.width).toBe(752) // 768 - 2*8, not the requested 860
    expect(w.position.x + w.size.width).toBeLessThanOrEqual(768)
  })
})

describe('the clamp stops at OPEN_WINDOW — the boundary is the decision', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('does not cage a window the user deliberately dragged off the right edge', () => {
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    const s = shellReducer(baseState([{ id: 1, appId: 'terminal', position: { x: 40, y: 50 }, size: { width: 720, height: 500 }, minimized: false }]),
      { type: 'MOVE_WINDOW', id: 1, position: { x: 700, y: 900 } })
    // 700 + 720 = 1420, a long way off a 768px screen. That is an arrangement,
    // not a bug: the user put it there.
    expect(s.desktops['desktop-1'].windows[0].position).toEqual({ x: 700, y: 900 })
  })

  it('does not clamp a RESIZE either', () => {
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    const s = shellReducer(baseState([{ id: 1, appId: 'terminal', position: { x: 40, y: 50 }, size: { width: 720, height: 500 }, minimized: false }]),
      { type: 'RESIZE_WINDOW', id: 1, size: { width: 2000, height: 1800 } })
    expect(s.desktops['desktop-1'].windows[0].size).toEqual({ width: 2000, height: 1800 })
  })

  it('restores a session onto a SMALLER screen proportionally, without clamping', () => {
    // The writer is a 2560x1440 desk; the reader is a 768-wide one. The
    // canonical unit already handles this: 0.5 of a viewport is 0.5 of any
    // viewport. A clamp here would be indistinguishable from the correct
    // answer for a centred window — so the fixture is a window deliberately
    // hanging off the writer's right edge, where the two differ.
    vi.stubGlobal('innerWidth', 2560)
    vi.stubGlobal('innerHeight', 1440)
    const persisted = serializableShellState(baseState([
      { id: 1, appId: 'terminal', position: { x: 2304, y: 144 }, size: { width: 1280, height: 720 }, minimized: false }, // 90% across, half the width
    ]))

    const hydrated = hydratePersistedState(persisted, { widthPx: viewportPx(768), heightPx: viewportPx(1024) })
    const w = hydrated.desktops['desktop-1'].windows[0]
    expect(w.position.x).toBeCloseTo(691.2)  // 0.9 * 768 — still 90% across
    expect(w.size.width).toBeCloseTo(384)    // 0.5 * 768 — still half the width
    // Still hanging off the right edge, exactly as the user left it. A clamp
    // would have pulled it back to 768 - 8 - 384 = 376.
    expect(w.position.x + w.size.width).toBeGreaterThan(768)
  })

  it('leaves the tiler alone: a window opened at 768 maximizes and restores to the same on-screen geometry', () => {
    // The fit must not fight tiling, and must not leave a restored window
    // somewhere it cannot be restored FROM. TILE/MAXIMIZE stash the pre-tile
    // floating geometry verbatim — which is now the fitted geometry, so the
    // round trip lands back on screen instead of back off the right edge.
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    let s = shellReducer(baseState(), { type: 'OPEN_WINDOW', appId: 'terminal' })
    const opened = s.desktops['desktop-1'].windows[0]
    s = shellReducer(s, { type: 'MAXIMIZE_WINDOW', id: opened.id })
    const max = s.desktops['desktop-1'].windows[0]
    expect(max._maximized).toBe(true)
    expect(max.size.width).toBe(768)
    s = shellReducer(s, { type: 'MAXIMIZE_WINDOW', id: opened.id })
    const restored = s.desktops['desktop-1'].windows[0]
    expect(restored.position).toEqual(opened.position)
    expect(restored.size).toEqual(opened.size)
    expect(restored.position.x + restored.size.width).toBeLessThanOrEqual(768)
  })

  it('a window opened on a phone-narrow screen does NOT come back shrunken on a big one', () => {
    // The corruption a clamp-at-hydrate would cause: the writer re-serializes
    // whatever is in state as a fraction of ITS OWN viewport, so any px the
    // reader clamped would be written back permanently. Round-trip through the
    // narrow screen twice, then read on a 2560 desk.
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    const opened = shellReducer(baseState(), { type: 'OPEN_WINDOW', appId: 'terminal' })
    const narrowView = readViewportSize()

    let persisted = serializableShellState(opened)
    for (let i = 0; i < 3; i++) {
      const back = hydratePersistedState(persisted, narrowView)
      persisted = serializableShellState({ ...opened, desktops: back.desktops })
    }

    const onBigScreen = hydratePersistedState(persisted, { widthPx: viewportPx(2560), heightPx: viewportPx(1440) })
    const w = onBigScreen.desktops['desktop-1'].windows[0]
    // 720/768 of the way across a 2560 desk — the same PROPORTION it was
    // opened at, scaled up, not 720 raw px and not anything smaller.
    expect(w.size.width).toBeCloseTo(720 / 768 * 2560)
    expect(w.size.width).toBeGreaterThan(720)
  })
})
