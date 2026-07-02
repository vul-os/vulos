// windowTiling.js — pure geometry + state-machine helpers for window snapping,
// keyboard tiling, and window cycling.
//
// Everything here is a pure function of its arguments (no DOM, no React) so the
// windowing behaviour can be unit-tested in isolation and reused by both the
// pointer-drag snap path (Window.jsx) and the keyboard-tiling path
// (useWindowShortcuts). The shell's menu bar occupies the top MENU_BAR_H px, so
// the usable area for tiling starts below it.

export const MENU_BAR_H = 32

// The set of concrete tile positions a window can occupy. 'maximize' behaves
// like 'top' geometrically but is tracked separately so it round-trips through
// the traffic-light maximize button and restore.
export const TILE_ZONES = [
  'left', 'right', 'top', 'maximize',
  'top-left', 'top-right', 'bottom-left', 'bottom-right',
]

/**
 * Geometry (position + size) for a tile zone within a viewport.
 * Returns null for an unknown zone. Widths/heights are computed so left+right
 * (and the four quarters) tile the usable area exactly with no seam.
 *
 * @param {string} zone   one of TILE_ZONES
 * @param {number} vw     viewport width
 * @param {number} vh     viewport height
 * @param {number} [menuBar=MENU_BAR_H] top inset reserved for the menu bar
 * @returns {{position:{x:number,y:number}, size:{width:number,height:number}}|null}
 */
export function tileGeometry(zone, vw, vh, menuBar = MENU_BAR_H) {
  const top = menuBar
  const usableH = vh - top
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
 * @returns {string|null} a TILE_ZONE (minus 'maximize'; the top edge maps to
 *   'top' which is full-screen) or null if the pointer isn't near an edge.
 */
export function snapZoneForPoint(x, y, vw, vh, edge) {
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
 *
 * @param {string|null} current  the window's current tile zone, or null
 * @param {'left'|'right'|'up'|'down'} dir
 * @returns {string|null}
 */
export function nextTile(current, dir) {
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
 * @param {Array<{id:any, minimized?:boolean}>} windows
 * @param {any} activeId  the currently-focused window id
 * @param {number} [dir=1]  +1 forward, -1 backward
 * @returns {any|null} the next window id, or null when nothing is cyclable
 */
export function nextWindowId(windows, activeId, dir = 1) {
  const visible = (windows || []).filter(w => !w.minimized)
  if (visible.length === 0) return null
  if (visible.length === 1) return visible[0].id
  const idx = visible.findIndex(w => w.id === activeId)
  const base = idx === -1 ? 0 : idx
  const n = ((base + dir) % visible.length + visible.length) % visible.length
  return visible[n].id
}
