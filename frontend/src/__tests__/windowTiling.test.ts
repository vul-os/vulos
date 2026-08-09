import { describe, it, expect } from 'vitest'
import {
  MENU_BAR_H, DOCK_H, TILE_ZONES,
  tileGeometry, snapZoneForPoint, nextTile, nextWindowId,
} from '../shell/windowTiling.js'

// The tiling module is the pure core of the window manager: geometry, the
// drag-edge snap detector, the keyboard-tiling state machine, and window
// cycling. These pin the behaviour the shell relies on.

const VW = 1000
const VH = 800

// The usable area is the viewport minus the menu bar at the top AND the dock at
// the bottom. The dock inset is the point of these expectations: without it a
// maximized window ran under the dock, which floats over its last 68px — and
// the dock's tiles take pointer events, so it was covering controls (the
// Assistant's composer, most visibly) rather than merely overlapping chrome.
const USABLE_H = VH - MENU_BAR_H - DOCK_H

describe('tileGeometry', () => {
  it('left + right halves tile the usable width with no seam or overlap', () => {
    const l = tileGeometry('left', VW, VH)
    const r = tileGeometry('right', VW, VH)
    if (!l) throw new Error('expected tileGeometry to resolve a geometry for "left"')
    if (!r) throw new Error('expected tileGeometry to resolve a geometry for "right"')
    expect(l.position).toEqual({ x: 0, y: MENU_BAR_H })
    expect(r.position.x).toBe(l.size.width)
    expect(l.size.width + r.size.width).toBe(VW)
    // Both span the full usable height (below the menu bar, above the dock).
    expect(l.size.height).toBe(USABLE_H)
    expect(r.size.height).toBe(USABLE_H)
  })

  it('maximize / top fill the usable area between the menu bar and the dock', () => {
    for (const zone of ['maximize', 'top']) {
      const g = tileGeometry(zone, VW, VH)
      if (!g) throw new Error(`expected tileGeometry to resolve a geometry for "${zone}"`)
      expect(g.position).toEqual({ x: 0, y: MENU_BAR_H })
      expect(g.size).toEqual({ width: VW, height: USABLE_H })
      // The property that matters, stated directly: the window's bottom edge
      // must clear the dock rather than run beneath it.
      expect(g.position.y + g.size.height).toBeLessThanOrEqual(VH - DOCK_H)
    }
  })

  it('the four quarters tile the usable area exactly', () => {
    const tl = tileGeometry('top-left', VW, VH)
    const tr = tileGeometry('top-right', VW, VH)
    const bl = tileGeometry('bottom-left', VW, VH)
    const br = tileGeometry('bottom-right', VW, VH)
    if (!tl) throw new Error('expected tileGeometry to resolve a geometry for "top-left"')
    if (!tr) throw new Error('expected tileGeometry to resolve a geometry for "top-right"')
    if (!bl) throw new Error('expected tileGeometry to resolve a geometry for "bottom-left"')
    if (!br) throw new Error('expected tileGeometry to resolve a geometry for "bottom-right"')
    // Columns meet at the horizontal midpoint.
    expect(tl.size.width + tr.size.width).toBe(VW)
    expect(bl.size.width + br.size.width).toBe(VW)
    // Rows stack to the full usable height.
    expect(tl.size.height + bl.size.height).toBe(USABLE_H)
    expect(tr.size.height + br.size.height).toBe(USABLE_H)
    // Bottom row starts where the top row ends.
    expect(bl.position.y).toBe(MENU_BAR_H + tl.size.height)
    // And the bottom row clears the dock.
    expect(bl.position.y + bl.size.height).toBeLessThanOrEqual(VH - DOCK_H)
    expect(br.position.y + br.size.height).toBeLessThanOrEqual(VH - DOCK_H)
  })

  it('respects custom menu-bar and dock insets', () => {
    // Both insets zeroed — the surface a caller with no chrome would want.
    const g = tileGeometry('left', VW, VH, 0, 0)
    if (!g) throw new Error('expected tileGeometry to resolve a geometry for "left"')
    expect(g.position.y).toBe(0)
    expect(g.size.height).toBe(VH)

    // A custom dock inset is honoured independently of the menu bar.
    const d = tileGeometry('left', VW, VH, 0, 120)
    if (!d) throw new Error('expected tileGeometry to resolve a geometry for "left"')
    expect(d.size.height).toBe(VH - 120)
  })

  it('returns null for an unknown zone', () => {
    expect(tileGeometry('nope', VW, VH)).toBeNull()
  })

  it('every declared TILE_ZONE has geometry', () => {
    for (const zone of TILE_ZONES) {
      expect(tileGeometry(zone, VW, VH)).not.toBeNull()
    }
  })
})

describe('snapZoneForPoint', () => {
  const edge = 20
  it('detects the four edges', () => {
    expect(snapZoneForPoint(5, 400, VW, VH, edge)).toBe('left')
    expect(snapZoneForPoint(995, 400, VW, VH, edge)).toBe('right')
    expect(snapZoneForPoint(500, 5, VW, VH, edge)).toBe('top')
    expect(snapZoneForPoint(500, 400, VW, VH, edge)).toBeNull()
  })

  it('corners win over edges', () => {
    expect(snapZoneForPoint(2, 2, VW, VH, edge)).toBe('top-left')
    expect(snapZoneForPoint(998, 2, VW, VH, edge)).toBe('top-right')
    expect(snapZoneForPoint(2, 798, VW, VH, edge)).toBe('bottom-left')
    expect(snapZoneForPoint(998, 798, VW, VH, edge)).toBe('bottom-right')
  })
})

describe('nextTile — keyboard tiling state machine', () => {
  it('from floating: arrows pick halves, up maximizes, down restores', () => {
    expect(nextTile(null, 'left')).toBe('left')
    expect(nextTile(null, 'right')).toBe('right')
    expect(nextTile(null, 'up')).toBe('maximize')
    expect(nextTile(null, 'down')).toBe('restore')
  })

  it('half + vertical arrow narrows to a quarter', () => {
    expect(nextTile('left', 'up')).toBe('top-left')
    expect(nextTile('left', 'down')).toBe('bottom-left')
    expect(nextTile('right', 'up')).toBe('top-right')
    expect(nextTile('right', 'down')).toBe('bottom-right')
  })

  it('a quarter can widen back to a half', () => {
    expect(nextTile('top-left', 'down')).toBe('left')
    expect(nextTile('bottom-left', 'up')).toBe('left')
    expect(nextTile('top-right', 'down')).toBe('right')
    expect(nextTile('bottom-right', 'up')).toBe('right')
  })

  it('left/right flip a window to the opposite side', () => {
    expect(nextTile('left', 'right')).toBe('right')
    expect(nextTile('right', 'left')).toBe('left')
    expect(nextTile('top-right', 'left')).toBe('top-left')
    expect(nextTile('bottom-left', 'right')).toBe('bottom-right')
  })

  it('down from maximize restores', () => {
    expect(nextTile('maximize', 'down')).toBe('restore')
  })
})

describe('nextWindowId — cycling', () => {
  const wins = [{ id: 1 }, { id: 2, minimized: true }, { id: 3 }, { id: 4 }]

  it('skips minimized windows and wraps forward', () => {
    expect(nextWindowId(wins, 1)).toBe(3)
    expect(nextWindowId(wins, 3)).toBe(4)
    expect(nextWindowId(wins, 4)).toBe(1) // wrap
  })

  it('cycles backward', () => {
    expect(nextWindowId(wins, 1, -1)).toBe(4)
    expect(nextWindowId(wins, 3, -1)).toBe(1)
  })

  it('handles no / single cyclable window', () => {
    expect(nextWindowId([], 1)).toBeNull()
    expect(nextWindowId([{ id: 9, minimized: true }], 9)).toBeNull()
    expect(nextWindowId([{ id: 7 }], 7)).toBe(7)
  })

  it('starts from the first window when the active id is unknown', () => {
    expect(nextWindowId(wins, 999)).toBe(3) // base index 0 (id 1) → +1 → id 3
  })
})
