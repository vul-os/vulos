// windowTiling.js — pure geometry + state-machine helpers for window snapping,
// keyboard tiling, and window cycling.
//
// Everything here is a pure function of its arguments (no DOM, no React) so the
// windowing behaviour can be unit-tested in isolation and reused by both the
// pointer-drag snap path (Window.jsx) and the keyboard-tiling path
// (useWindowShortcuts). The shell's menu bar occupies the top MENU_BAR_H px, so
// the usable area for tiling starts below it.

export const MENU_BAR_H = 32

// Bottom inset reserved for the dock: its 58px toolbar plus the 10px
// (0.625rem) it floats above the viewport edge — measured from the rendered
// element, not guessed. See Dock.tsx's wrapper.
//
// Without this, tiling and maximize sized every window to the full viewport
// height and the dock — which is now ALWAYS present, not just when a window
// exists — floated over the bottom 68px of it. In a maximized Assistant that
// put the dock across the composer: the input and the "sent to a third party"
// notice underneath it were both partly hidden, and the tiles sit on
// pointer-events:auto, so it was covering a control the user needed to click,
// not merely overlapping decoration. Every maximized app had it.
export const DOCK_H = 68

// The set of concrete tile positions a window can occupy. 'maximize' behaves
// like 'top' geometrically but is tracked separately so it round-trips through
// the traffic-light maximize button and restore.
export const TILE_ZONES = [
  'left', 'right', 'top', 'maximize',
  'top-left', 'top-right', 'bottom-left', 'bottom-right',
] as const

export type TileZone = typeof TILE_ZONES[number]

export interface TileGeometryResult {
  position: { x: number; y: number }
  size: { width: number; height: number }
}

/**
 * Geometry (position + size) for a tile zone within a viewport.
 * Returns null for an unknown zone. Widths/heights are computed so left+right
 * (and the four quarters) tile the usable area exactly with no seam.
 *
 * @param zone   one of TILE_ZONES
 * @param vw     viewport width
 * @param vh     viewport height
 * @param menuBar top inset reserved for the menu bar
 * @param dock    bottom inset reserved for the dock
 */
export function tileGeometry(zone: string, vw: number, vh: number, menuBar: number = MENU_BAR_H, dock: number = DOCK_H): TileGeometryResult | null {
  const top = menuBar
  const usableH = vh - top - dock
  const halfW = Math.floor(vw / 2)
  const rightW = vw - halfW
  const halfH = Math.floor(usableH / 2)
  const bottomH = usableH - halfH
  switch (zone) {
    case 'left':
      return { position: { x: 0, y: top }, size: { width: halfW, height: usableH } }
    case 'right':
      return { position: { x: halfW, y: top }, size: { width: rightW, height: usableH } }
    case 'top':
    case 'maximize':
      return { position: { x: 0, y: top }, size: { width: vw, height: usableH } }
    case 'top-left':
      return { position: { x: 0, y: top }, size: { width: halfW, height: halfH } }
    case 'top-right':
      return { position: { x: halfW, y: top }, size: { width: rightW, height: halfH } }
    case 'bottom-left':
      return { position: { x: 0, y: top + halfH }, size: { width: halfW, height: bottomH } }
    case 'bottom-right':
      return { position: { x: halfW, y: top + halfH }, size: { width: rightW, height: bottomH } }
    default:
      return null
  }
}

/**
 * Detect which snap zone a pointer at (x, y) falls into during a drag, given an
 * edge-trigger threshold. Corners take precedence over edges.
 *
 * @returns a TILE_ZONE (minus 'maximize'; the top edge maps to 'top' which is
 *   full-screen) or null if the pointer isn't near an edge.
 */
export function snapZoneForPoint(x: number, y: number, vw: number, vh: number, edge: number): string | null {
  const isLeft = x <= edge
  const isRight = x >= vw - edge
  const isTop = y <= edge
  const isBottom = y >= vh - edge
  if (isLeft && isTop) return 'top-left'
  if (isRight && isTop) return 'top-right'
  if (isLeft && isBottom) return 'bottom-left'
  if (isRight && isBottom) return 'bottom-right'
  if (isLeft) return 'left'
  if (isRight) return 'right'
  if (isTop) return 'top'
  return null
}

/**
 * Keyboard-tiling state machine. Given the window's current tile (or null when
 * it's floating) and an arrow direction, return the next tile — mirroring the
 * familiar Windows-snap / GNOME progression:
 *
 *   ·          → Left  → left half   → Up   → top-left quarter  → …
 *   maximize   ← Up  ←  (from floating / any half)
 *   Down       → un-tiles a half's opposite corner, or 'restore' from maximize
 *
 * Returns a TILE_ZONE, or the sentinel 'restore' meaning "return the window to
 * its pre-tile floating geometry".
 */
export function nextTile(current: string | null, dir: 'left' | 'right' | 'up' | 'down'): string | null {
  switch (dir) {
    case 'up':
      if (current === 'left') return 'top-left'
      if (current === 'right') return 'top-right'
      if (current === 'bottom-left') return 'left'
      if (current === 'bottom-right') return 'right'
      return 'maximize'
    case 'down':
      if (current === 'left') return 'bottom-left'
      if (current === 'right') return 'bottom-right'
      if (current === 'top-left') return 'left'
      if (current === 'top-right') return 'right'
      return 'restore'
    case 'left':
      if (current === 'right') return 'left'
      if (current === 'top-right') return 'top-left'
      if (current === 'bottom-right') return 'bottom-left'
      return 'left'
    case 'right':
      if (current === 'left') return 'right'
      if (current === 'top-left') return 'top-right'
      if (current === 'bottom-left') return 'bottom-right'
      return 'right'
    default:
      return null
  }
}

/**
 * Pick the next window id to focus when cycling (Alt+`). Only non-minimized
 * windows participate; the order follows the given list. Wraps around.
 *
 * @param dir  +1 forward, -1 backward
 * @returns the next window id, or null when nothing is cyclable
 */
export function nextWindowId<T extends { id: unknown; minimized?: boolean }>(
  windows: T[] | null | undefined,
  activeId: unknown,
  dir: number = 1,
): unknown | null {
  const visible = (windows || []).filter(w => !w.minimized)
  if (visible.length === 0) return null
  if (visible.length === 1) return visible[0].id
  const idx = visible.findIndex(w => w.id === activeId)
  const base = idx === -1 ? 0 : idx
  const n = ((base + dir) % visible.length + visible.length) % visible.length
  return visible[n].id
}
