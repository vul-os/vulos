// shellStateGeometry.test.ts — proves the units bug traced in
// roadmap/SCREENS.md is actually fixed: serializableShellState/
// hydratePersistedState (ShellProvider.tsx) now carry window geometry as a
// canonical fraction of the WRITER's own viewport (screenScale.ts) instead of
// raw CSS px, and resolve it back through the READER's own viewport.
//
// Every cross-viewport case below uses TWO DIFFERENT extents on purpose,
// matching screenScale.test.ts's own rule: a suite where every viewport is
// the same size can't tell a correct conversion apart from one that just
// passes px through unchanged.

import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  shellReducer, serializableShellState, hydratePersistedState, saveShellState, loadShellState,
  type ShellWindow,
} from '../ShellProvider'
import { readViewportSize, viewportPx } from '../screenScale'
import {
  openWindowGeometry, DEFAULT_WINDOW_SIZE, OPEN_CASCADE_ORIGIN,
  MENU_BAR_H, DOCK_H, WINDOW_EDGE_MARGIN,
} from '../../shell/windowTiling'

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

// 'terminal' is a builtin app (see shell/builtinApps.tsx) — serializableShellState
// only keeps builtin/URL windows, so every fixture below uses it.
function win(over: Partial<ShellWindow> = {}): ShellWindow {
  return { id: 1, appId: 'terminal', title: 'Terminal', position: { x: 100, y: 100 }, size: { width: 720, height: 500 }, minimized: false, ...over }
}

function firstWindow(hydrated: ReturnType<typeof hydratePersistedState>): ShellWindow {
  return hydrated.desktops['desktop-1'].windows[0]
}

afterEach(() => vi.unstubAllGlobals())

describe('serializableShellState — the writer side', () => {
  it('tags geometry canonical-v1 and stores a fraction of the writer\'s own viewport, not raw px', () => {
    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    const persisted = serializableShellState(baseState([win({ position: { x: 1728, y: 54 }, size: { width: 384, height: 216 } })]))
    const w = persisted.desktops['desktop-1'].windows[0]
    expect(w.geomUnit).toBe('canonical-v1')
    // 1728/1920 = 0.9, 54/1080 = 0.05, 384/1920 = 0.2, 216/1080 = 0.2 — fractions,
    // nothing near the original px values.
    expect(w.position.x).toBeCloseTo(0.9)
    expect(w.position.y).toBeCloseTo(0.05)
    expect(w.size.width).toBeCloseTo(0.2)
    expect(w.size.height).toBeCloseTo(0.2)
  })
})

describe('cross-viewport publish/apply — a 1920x1080 writer, a 3840x2160 follower', () => {
  it('lands a window proportionally correct on the follower, not at the writer\'s raw px', () => {
    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    // 90% across, 5% down, a 400x300 window on the writer.
    const persisted = serializableShellState(baseState([win({ position: { x: 1728, y: 54 }, size: { width: 400, height: 300 } })]))

    vi.stubGlobal('innerWidth', 3840)
    vi.stubGlobal('innerHeight', 2160)
    const hydrated = hydratePersistedState(persisted, readViewportSize())
    const w = firstWindow(hydrated)

    // Same relative place on the 4K follower: 90% of 3840, 5% of 2160.
    expect(w.position.x).toBeCloseTo(3456)
    expect(w.position.y).toBeCloseTo(108)
    // Same relative footprint, not the writer's raw 400x300.
    expect(w.size.width).toBeCloseTo(800)
    expect(w.size.height).toBeCloseTo(600)

    // The bug this whole task exists to fix: a raw-px passthrough would have
    // left position at (1728, 54) verbatim.
    expect(w.position.x).not.toBeCloseTo(1728)
    expect(w.position.y).not.toBeCloseTo(54)
  })

  it('the reverse direction: a 4K writer\'s window stays on-screen for a 1080p follower instead of hanging off the edge', () => {
    vi.stubGlobal('innerWidth', 3840)
    vi.stubGlobal('innerHeight', 2160)
    // Near the 4K writer's right edge.
    const persisted = serializableShellState(baseState([win({ position: { x: 3600, y: 100 }, size: { width: 500, height: 400 } })]))

    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    const hydrated = hydratePersistedState(persisted, readViewportSize())
    const w = firstWindow(hydrated)

    // 3600 raw px would be almost double the 1080p follower's own width
    // (entirely off-screen). The canonical conversion keeps it near the same
    // relative edge instead.
    expect(w.position.x).toBeLessThan(1920)
    expect(w.position.x).toBeCloseTo(1800) // 3600/3840 * 1920
    expect(w.size.width).toBeCloseTo(250)  // 500/3840 * 1920
  })
})

// A v0.1.0-shaped persisted blob: exactly the fields that release's
// saveShellState wrote (see `git show v0.1.0:.../ShellProvider.tsx`), with NO
// geomUnit and — the fact the whole decision turns on — no record of the
// viewport that produced the numbers.
function legacyBlob(position: { x: number; y: number }, size: { width: number; height: number }) {
  return {
    desktops: {
      'desktop-1': {
        id: 'desktop-1', label: 'Desktop 1', activeWindow: 1,
        windows: [{ id: 1, appId: 'terminal', position, size, minimized: false, _tile: null, _maximized: false, _builtin: true }],
      },
    },
    activeDesktop: 'desktop-1',
  }
}

const extent = (w: number, h: number) => ({ widthPx: viewportPx(w), heightPx: viewportPx(h) })

describe('legacy payload — a pre-canonical-unit writer (v0.1.0), or one mid-upgrade', () => {
  // THE DECISION, recorded where it is checked. Untagged geometry is raw px
  // from a viewport that was never written down, so it is:
  //   - never RESCALED (there is no writer extent to divide by, and magnitude
  //     cannot infer one — that is why the tag exists), and
  //   - never DISCARDED (that would throw away the arrangement of every
  //     v0.1.0 user upgrading on the same monitor, which is nearly all of
  //     them),
  // but FITTED to the reading viewport, because a window the user cannot
  // reach is worse than a window that moved. See the "What an UNTAGGED
  // payload is, and why it is fitted" section of ShellProvider.tsx.

  it('an untagged payload is never rescaled — a window that already fits comes back byte-identical', () => {
    // A follower on a totally different (4K) viewport — if this were
    // mistaken for canonical, 1800/40 would be multiplied by the extent and
    // land far off-screen instead of passing through untouched. It fits a
    // 4K viewport as-is, so the fit is a no-op and the passthrough this test
    // has always pinned still holds exactly.
    const hydrated = hydratePersistedState(legacyBlob({ x: 1800, y: 40 }, { width: 720, height: 500 }), extent(3840, 2160))
    const w = firstWindow(hydrated)
    expect(w.position).toEqual({ x: 1800, y: 40 })
    expect(w.size).toEqual({ width: 720, height: 500 })
  })

  it('a legacy blob re-read on the SAME screen that wrote it changes nothing at all', () => {
    // The v0.1.0 → v0.2.0 upgrade as nearly every user experiences it: same
    // box, same monitor. Fractional coordinates on purpose — the fit must not
    // round a geometry it is leaving alone.
    const hydrated = hydratePersistedState(legacyBlob({ x: 837.5, y: 271.25 }, { width: 733.5, height: 419.75 }), extent(1920, 1080))
    const w = firstWindow(hydrated)
    expect(w.position).toEqual({ x: 837.5, y: 271.25 })
    expect(w.size).toEqual({ width: 733.5, height: 419.75 })
  })

  it('the 1920→768 case: a legacy window that would restore 1032px off the right edge is brought back on screen', () => {
    // Written at x=1800 on a 1920-wide screen; the box is now opened on a
    // 768-wide one. Passing it through would put the title bar AND the
    // bottom-right resize grip past the right edge — no gesture reaches it.
    const hydrated = hydratePersistedState(legacyBlob({ x: 1800, y: 40 }, { width: 720, height: 500 }), extent(768, 1024))
    const w = firstWindow(hydrated)
    expect(w.position.x + w.size.width).toBeLessThanOrEqual(768 - WINDOW_EDGE_MARGIN)
    expect(w.position.x).toBe(768 - WINDOW_EDGE_MARGIN - 720) // 40
    // Fitted, NOT rescaled: 1800 * 768/1920 would be 720, and the size is
    // untouched because it already fits.
    expect(w.position.x).not.toBe(720)
    expect(w.size).toEqual({ width: 720, height: 500 })
    // And not discarded either — y was reachable, so y is exactly what the
    // user left it at.
    expect(w.position.y).toBe(40)
  })

  it('a legacy window larger than the whole screen shrinks to fit rather than overflowing it', () => {
    const hydrated = hydratePersistedState(legacyBlob({ x: 200, y: 900 }, { width: 1600, height: 1000 }), extent(768, 1024))
    const w = firstWindow(hydrated)
    expect(w.size).toEqual({ width: 768 - 2 * WINDOW_EDGE_MARGIN, height: 1024 - MENU_BAR_H - DOCK_H }) // 752 x 924
    expect(w.position).toEqual({ x: WINDOW_EDGE_MARGIN, y: MENU_BAR_H })
    expect(w.position.y + w.size.height).toBe(1024 - DOCK_H)
  })

  it('no legacy geometry, from any plausible writer screen, restores outside the usable band of any viewport the canvas mounts at', () => {
    // The general property, table-driven, because the two cases above are
    // single points and the guarantee is "always reachable". Viewports are
    // windowOpenFit/windows-open-geometry's own set — 768 is the narrowest
    // width the desktop canvas is ever mounted at.
    const viewports = [[768, 1024], [834, 1194], [1024, 768], [1440, 900], [1920, 1080]] as const
    const legacyGeoms = [
      [{ x: 1800, y: 40 }, { width: 720, height: 500 }],
      [{ x: 3600, y: 1900 }, { width: 500, height: 400 }],
      [{ x: -400, y: -200 }, { width: 720, height: 500 }],
      [{ x: 0, y: 0 }, { width: 3800, height: 2100 }],
      [{ x: 60, y: 50 }, { width: 720, height: 500 }],
    ] as const
    const offenders: string[] = []
    for (const [vw, vh] of viewports) {
      for (const [position, size] of legacyGeoms) {
        const w = firstWindow(hydratePersistedState(legacyBlob(position, size), extent(vw, vh)))
        const outside = w.position.x < WINDOW_EDGE_MARGIN || w.position.y < MENU_BAR_H ||
          w.position.x + w.size.width > vw - WINDOW_EDGE_MARGIN ||
          w.position.y + w.size.height > vh - DOCK_H
        if (outside) offenders.push(`${vw}x${vh}: (${position.x},${position.y}) ${size.width}x${size.height} -> (${w.position.x},${w.position.y}) ${w.size.width}x${w.size.height}`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('fits a legacy window exactly the way openWindowGeometry fits a fresh one', () => {
    // The anti-drift check: the legacy fit and the open clamp answer the same
    // question ("where does the shell put a window on THIS screen") and live
    // in two different files. Given the same input geometry the open cascade
    // would produce, they must agree — insets, order of operations and all.
    for (const [vw, vh] of [[768, 1024], [1024, 768], [1440, 900]] as const) {
      const hydrated = hydratePersistedState(legacyBlob({ ...OPEN_CASCADE_ORIGIN }, { ...DEFAULT_WINDOW_SIZE }), extent(vw, vh))
      const w = firstWindow(hydrated)
      const fresh = openWindowGeometry(0, DEFAULT_WINDOW_SIZE, extent(vw, vh))
      expect({ position: w.position, size: w.size }, `${vw}x${vh}`).toEqual(fresh)
    }
  })

  it('a legacy window with missing/NaN geometry gets the default rather than a NaN rect', () => {
    // hydrateGeometry's untagged branch used to hand these straight through;
    // it is now the first thing that dereferences numbers parsed out of
    // foreign JSON, so a corrupt blob must land on the same fallback a
    // corrupt canonical payload does.
    const nan = hydratePersistedState(legacyBlob({ x: NaN, y: 40 }, { width: 720, height: 500 }), extent(1920, 1080))
    expect(firstWindow(nan).position).toEqual({ x: 60, y: 50 })
    const missing = hydratePersistedState(
      // @ts-expect-error -- a hand-corrupted blob: position absent entirely, which the JSON boundary cannot rule out
      legacyBlob(undefined, { width: 720, height: 500 }), extent(1920, 1080))
    expect(firstWindow(missing).size).toEqual({ width: 720, height: 500 })
  })

  it('loadShellState brings a real v0.1.0 localStorage blob back on screen end to end', () => {
    // Not hydratePersistedState directly: the actual boot path, through the
    // real STORAGE_KEY, with the JSON a v0.1.0 box would have left behind.
    localStorage.clear()
    localStorage.setItem('vulos-shell-state', JSON.stringify(legacyBlob({ x: 1800, y: 40 }, { width: 720, height: 500 })))
    vi.stubGlobal('innerWidth', 768)
    vi.stubGlobal('innerHeight', 1024)
    const loaded = loadShellState()
    if (!loaded) throw new Error('expected loadShellState to restore the legacy blob, not drop it')
    const w = loaded.desktops['desktop-1'].windows[0]
    expect(w.position.x + w.size.width).toBeLessThanOrEqual(768 - WINDOW_EDGE_MARGIN)
    expect(w.position.y).toBeGreaterThanOrEqual(MENU_BAR_H)
    localStorage.clear()
  })
})

describe('null viewport — this tab cannot read window.innerWidth/innerHeight (SSR/tests)', () => {
  it('serializableShellState falls back to untagged raw px rather than fabricating a canonical fraction', () => {
    const realWindow = globalThis.window
    // @ts-expect-error -- simulating an environment without `window`, mirroring screenScale.test.ts's own pattern
    delete globalThis.window
    vi.stubGlobal('innerWidth', undefined)
    vi.stubGlobal('innerHeight', undefined)
    try {
      const persisted = serializableShellState(baseState([win({ position: { x: 100, y: 100 }, size: { width: 720, height: 500 } })]))
      const w = persisted.desktops['desktop-1'].windows[0]
      expect(w.geomUnit).toBeUndefined()
      // Raw px, unchanged — never a fraction computed against an unknown extent.
      expect(w.position).toEqual({ x: 100, y: 100 })
      expect(w.size).toEqual({ width: 720, height: 500 })
    } finally {
      globalThis.window = realWindow
    }
  })

  it('hydratePersistedState falls back to a safe default rather than treating a canonical fraction as raw px', () => {
    const persisted = {
      desktops: {
        'desktop-1': {
          id: 'desktop-1', label: 'Desktop 1', activeWindow: 1,
          windows: [{ id: 1, appId: 'terminal', geomUnit: 'canonical-v1' as const, position: { x: 0.5, y: 0.5 }, size: { width: 0.3, height: 0.4 }, minimized: false, _tile: null, _maximized: false, _builtin: true }],
        },
      },
      activeDesktop: 'desktop-1',
    }
    const hydrated = hydratePersistedState(persisted, null)
    const w = firstWindow(hydrated)
    // NOT the raw fraction values misread as px (which would place a 0.3x0.4
    // px window at the top-left corner) — the shell's own default geometry.
    expect(w.position).toEqual({ x: 60, y: 50 })
    expect(w.size).toEqual({ width: 720, height: 500 })
  })

  it('a canonical-tagged payload with implausible numbers (corrupted, not just legacy) also falls back rather than being trusted', () => {
    const persisted = {
      desktops: {
        'desktop-1': {
          id: 'desktop-1', label: 'Desktop 1', activeWindow: 1,
          // Tagged canonical but the numbers are raw-px-shaped (thousands) —
          // isPlausibleCanonicalUnit's backstop, not the tag alone, catches this.
          windows: [{ id: 1, appId: 'terminal', geomUnit: 'canonical-v1' as const, position: { x: 1800, y: 40 }, size: { width: 720, height: 500 }, minimized: false, _tile: null, _maximized: false, _builtin: true }],
        },
      },
      activeDesktop: 'desktop-1',
    }
    const hydrated = hydratePersistedState(persisted, { widthPx: viewportPx(1920), heightPx: viewportPx(1080) })
    const w = firstWindow(hydrated)
    expect(w.position).toEqual({ x: 60, y: 50 })
    expect(w.size).toEqual({ width: 720, height: 500 })
  })
})

describe('round-tripping on the SAME viewport', () => {
  it('does not drift converting to canonical and back once', () => {
    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    const persisted = serializableShellState(baseState([win({ position: { x: 837, y: 271 }, size: { width: 733, height: 419 } })]))
    const hydrated = hydratePersistedState(persisted, readViewportSize())
    const w = firstWindow(hydrated)
    expect(w.position.x).toBeCloseTo(837, 6)
    expect(w.position.y).toBeCloseTo(271, 6)
    expect(w.size.width).toBeCloseTo(733, 6)
    expect(w.size.height).toBeCloseTo(419, 6)
  })

  it('does not accumulate drift across repeated publish cycles', () => {
    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    let position = { x: 837, y: 271 }
    let size = { width: 733, height: 419 }
    for (let i = 0; i < 8; i++) {
      const persisted = serializableShellState(baseState([win({ position, size })]))
      const hydrated = hydratePersistedState(persisted, readViewportSize())
      const w = firstWindow(hydrated)
      position = w.position
      size = w.size
    }
    expect(position.x).toBeCloseTo(837, 6)
    expect(position.y).toBeCloseTo(271, 6)
    expect(size.width).toBeCloseTo(733, 6)
    expect(size.height).toBeCloseTo(419, 6)
  })

  it('a same-tab localStorage reload (saveShellState/loadShellState) is unaffected — single-screen behaviour does not regress', () => {
    localStorage.clear()
    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    saveShellState(baseState([win({ position: { x: 300, y: 150 }, size: { width: 640, height: 480 } })]))
    const loaded = loadShellState()
    if (!loaded) throw new Error('expected loadShellState to return persisted state')
    const w = loaded.desktops['desktop-1'].windows[0]
    expect(w.position.x).toBeCloseTo(300, 6)
    expect(w.position.y).toBeCloseTo(150, 6)
    expect(w.size.width).toBeCloseTo(640, 6)
    expect(w.size.height).toBeCloseTo(480, 6)
  })

  it('a reload after the browser window itself was resized rescales proportionally instead of leaving the saved position meaningless', () => {
    localStorage.clear()
    vi.stubGlobal('innerWidth', 1920)
    vi.stubGlobal('innerHeight', 1080)
    saveShellState(baseState([win({ position: { x: 960, y: 540 }, size: { width: 400, height: 300 } })])) // dead center
    vi.stubGlobal('innerWidth', 3840)
    vi.stubGlobal('innerHeight', 2160)
    const loaded = loadShellState()
    if (!loaded) throw new Error('expected loadShellState to return persisted state')
    const w = loaded.desktops['desktop-1'].windows[0]
    // Still dead center of the now-larger viewport, not the stale 960x540.
    expect(w.position.x).toBeCloseTo(1920)
    expect(w.position.y).toBeCloseTo(1080)
  })
})
