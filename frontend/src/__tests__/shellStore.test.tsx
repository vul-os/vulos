import { describe, it, expect, beforeEach } from 'vitest'
import { shellReducer, saveShellState, loadShellState, type ShellWindow } from '../providers/ShellProvider.jsx'
import { tileGeometry } from '../shell/windowTiling.js'

// The shell reducer is the window-manager store. These pin the windowing
// behaviours the desktop depends on: tiling with restore, freeing a tile on
// manual move, and persisting/re-hydrating URL + builtin windows across reload.

// shellReducer's state param type (ShellState) isn't exported — derive it from
// the function signature instead of duplicating/re-declaring it here.
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

function win(over: Partial<ShellWindow> = {}): ShellWindow {
  return { id: 1, appId: 'terminal', title: 'Terminal', position: { x: 100, y: 100 }, size: { width: 720, height: 500 }, minimized: false, ...over }
}

describe('shellReducer — TILE_WINDOW', () => {
  it('applies geometry, records the tile, and preserves the floating geometry', () => {
    const g = tileGeometry('left', 1000, 800)
    if (!g) throw new Error('expected tileGeometry to resolve a geometry for "left"')
    const s = shellReducer(baseState([win()]), { type: 'TILE_WINDOW', id: 1, zone: 'left', geometry: g })
    const w = s.desktops['desktop-1'].windows[0]
    expect(w.position).toEqual(g.position)
    expect(w.size).toEqual(g.size)
    expect(w._tile).toBe('left')
    // Original floating geometry stashed for restore.
    expect(w._prevPosition).toEqual({ x: 100, y: 100 })
    expect(w._prevSize).toEqual({ width: 720, height: 500 })
  })

  it('keeps the ORIGINAL floating geometry when re-tiling an already-tiled window', () => {
    let s = shellReducer(baseState([win()]), { type: 'TILE_WINDOW', id: 1, zone: 'left', geometry: tileGeometry('left', 1000, 800) })
    s = shellReducer(s, { type: 'TILE_WINDOW', id: 1, zone: 'top-left', geometry: tileGeometry('top-left', 1000, 800) })
    const w = s.desktops['desktop-1'].windows[0]
    expect(w._tile).toBe('top-left')
    expect(w._prevPosition).toEqual({ x: 100, y: 100 }) // unchanged
  })

  it('restore returns the window to its floating geometry and clears tile state', () => {
    let s = shellReducer(baseState([win()]), { type: 'TILE_WINDOW', id: 1, zone: 'maximize', geometry: tileGeometry('maximize', 1000, 800) })
    s = shellReducer(s, { type: 'TILE_WINDOW', id: 1, zone: 'restore', geometry: null })
    const w = s.desktops['desktop-1'].windows[0]
    expect(w.position).toEqual({ x: 100, y: 100 })
    expect(w.size).toEqual({ width: 720, height: 500 })
    expect(w._tile).toBeNull()
    expect(w._maximized).toBe(false)
  })

  it('restore is a no-op for a never-tiled window', () => {
    const s = shellReducer(baseState([win()]), { type: 'TILE_WINDOW', id: 1, zone: 'restore', geometry: null })
    expect(s.desktops['desktop-1'].windows[0].position).toEqual({ x: 100, y: 100 })
  })

  it('manually moving a tiled window frees its tile', () => {
    let s = shellReducer(baseState([win()]), { type: 'TILE_WINDOW', id: 1, zone: 'left', geometry: tileGeometry('left', 1000, 800) })
    s = shellReducer(s, { type: 'MOVE_WINDOW', id: 1, position: { x: 5, y: 5 } })
    expect(s.desktops['desktop-1'].windows[0]._tile).toBeNull()
  })
})

describe('shell persistence — save / load / rehydrate', () => {
  beforeEach(() => localStorage.clear())

  it('persists URL and builtin windows, dropping live-session windows', () => {
    const state = baseState([
      win({ id: 1, appId: 'terminal' }),                                   // builtin
      { id: 2, appId: 'webthing', title: 'Web', url: 'https://x/', position: { x: 0, y: 0 }, size: { width: 400, height: 300 }, minimized: false }, // url
      { id: 3, appId: 'browser', title: 'Browser', component: <div />, position: { x: 0, y: 0 }, size: { width: 400, height: 300 }, minimized: false }, // live -> dropped
    ])
    saveShellState(state)
    const loaded = loadShellState()
    if (!loaded) throw new Error('expected loadShellState to return persisted state')
    const ids = loaded.desktops['desktop-1'].windows.map(w => w.id).sort()
    expect(ids).toEqual([1, 2])
    const builtin = loaded.desktops['desktop-1'].windows.find(w => w.id === 1)
    expect(builtin?._builtin).toBe(true)
  })

  it('RESTORE_STATE rebuilds a live React component for a persisted builtin', () => {
    saveShellState(baseState([win({ id: 7, appId: 'terminal', minimized: true })]))
    const saved = loadShellState()
    if (!saved) throw new Error('expected loadShellState to return persisted state')
    // Persisted form carries no component…
    expect(saved.desktops['desktop-1'].windows[0].component).toBeUndefined()
    const s = shellReducer(baseState(), { type: 'RESTORE_STATE', saved })
    const w = s.desktops['desktop-1'].windows[0]
    // …but RESTORE_STATE re-hydrates one from the appId.
    expect(w.component).not.toBeNull()
    expect(w.minimized).toBe(true)
  })
})
