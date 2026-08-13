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

describe('legacy payload — a pre-canonical-unit writer, or one mid-upgrade', () => {
  it('an untagged payload is treated as already this viewport\'s raw px, not rescaled', () => {
    const legacy = {
      desktops: {
        'desktop-1': {
          id: 'desktop-1', label: 'Desktop 1', activeWindow: 1,
          windows: [{ id: 1, appId: 'terminal', position: { x: 1800, y: 40 }, size: { width: 720, height: 500 }, minimized: false, _tile: null, _maximized: false, _builtin: true }],
        },
      },
      activeDesktop: 'desktop-1',
    }
    // A follower on a totally different (4K) viewport — if this were
    // mistaken for canonical, 1800/40 would be multiplied by the extent and
    // land far off-screen instead of passing through untouched.
    const hydrated = hydratePersistedState(legacy, { widthPx: viewportPx(3840), heightPx: viewportPx(2160) })
    const w = firstWindow(hydrated)
    expect(w.position).toEqual({ x: 1800, y: 40 })
    expect(w.size).toEqual({ width: 720, height: 500 })
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
