// windowTiling.js — pure geometry + state-machine helpers for window snapping,
// keyboard tiling, and window cycling.
//
// Every FUNCTION here is a pure function of its arguments (no DOM, no React) so
// the windowing behaviour can be unit-tested in isolation and reused by both the
// pointer-drag snap path (Window.jsx) and the keyboard-tiling path
// (useWindowShortcuts). The shell's menu bar occupies the top MENU_BAR_H px, so
// the usable area for tiling starts below it.
//
// ONE exception, stated here rather than discovered: MENU_BAR_H is read from the
// `--menubar-h` CSS token at import, because it has to agree with the bar the
// user is actually looking at. See its own comment below. The functions still
// take the inset as a parameter; this is only the default they fall back to.

/**
 * The menu bar's height, READ FROM THE TOKEN THAT DRAWS IT.
 *
 * ── Why this one value is not a literal ─────────────────────────────────────
 *
 * The number 32 used to live in three files that had no way of knowing about
 * each other: `h-8` in shell/TopBar.tsx (the bar), `pt-8` in
 * layouts/DesktopCanvas.tsx (the origin every window is positioned against) and
 * here (the geometry a window OPENS with, and the top of every tile zone).
 * `--menubar-h` already existed in src/index.css and only two unrelated rules in
 * shell-chrome.css read it.
 *
 * That was survivable while the answer was always 32. It stopped being
 * survivable when the bar had to become 44px on a coarse pointer so its
 * affordances could clear the touch floor — an iPad in landscape runs the
 * DESKTOP canvas (TOUCH_STACK_MAX is 1024) and its menu bar was six controls at
 * 28x28 and smaller. Growing the bar in two of the three places would have
 * opened every window 12px UNDERNEATH it, on exactly the devices the change is
 * for. So all three read the token, and this is where the number crosses from
 * CSS into JS.
 *
 * ── What this does to the file's purity claim ───────────────────────────────
 *
 * The header above says "no DOM". That is still true of every FUNCTION here —
 * they take `menuBar` as a parameter and this is only its default. What is no
 * longer true is that the module has no DOM read at all, and pretending
 * otherwise would be worse than saying so: this runs once, at import.
 *
 * Every failure mode resolves to 32, which is the value that was hard-coded
 * before, so the worst case is exactly today's behaviour rather than a new one:
 * no `window` (unit tests run in jsdom, where index.css is not loaded and the
 * property reads empty), an unparseable value, or a number outside a sane band.
 * The band is a tripwire on absurdity, not a measurement — the same shape as the
 * 96px inset bound in mobile/safeAreaInsets.ts.
 */
const MENU_BAR_H_FALLBACK = 32
const MENU_BAR_H_MAX = 96

function resolveMenuBarHeight(): number {
  if (typeof window === 'undefined' || typeof document === 'undefined') return MENU_BAR_H_FALLBACK
  try {
    const root = document.documentElement
    const raw = getComputedStyle(root).getPropertyValue('--menubar-h').trim()
    if (!raw) return MENU_BAR_H_FALLBACK
    // getPropertyValue returns a custom property's SPECIFIED value, so the unit
    // arrives as written — `2rem` from index.css, `44px` from the coarse-pointer
    // override. Both have to be understood; anything else falls back rather than
    // guessing, because a wrong number here is a window under the menu bar.
    const n = parseFloat(raw)
    if (!Number.isFinite(n)) return MENU_BAR_H_FALLBACK
    let px: number
    if (raw.endsWith('rem')) px = n * (parseFloat(getComputedStyle(root).fontSize) || 16)
    else if (raw.endsWith('px')) px = n
    else return MENU_BAR_H_FALLBACK
    if (!(px > 0) || px > MENU_BAR_H_MAX) return MENU_BAR_H_FALLBACK
    return Math.round(px)
  } catch {
    return MENU_BAR_H_FALLBACK
  }
}

export const MENU_BAR_H = resolveMenuBarHeight()

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

// ─── Opening geometry ───────────────────────────────────────────────────────
//
// Where a window lands the moment it is opened, and how big it starts.
//
// This used to be two expressions inlined in ShellProvider's OPEN_WINDOW case:
//   position: { x: 60 + (n % 6) * 32, y: 50 + (n % 6) * 32 }
//   size:     action.size || { width: 720, height: 500 }
// Neither consulted the viewport. Measured in a real browser (see
// e2e/windows-open-geometry.e2e.ts), on a 768px-wide viewport — the NARROWEST
// width at which the desktop canvas is ever mounted, see shell/viewportRule.ts
// — the first window's right edge landed at 780px and the sixth at 940px, i.e.
// 12px and 172px of the window off the right of the screen, taking the resize
// grip (bottom-right corner) with them for all six. On 834x1194 the third
// window onward overflowed by 10..106px. On 1024x768 the sixth window's bottom
// edge sat at 710px, 10px underneath the dock, which floats over the last 68px
// and takes the clicks.
//
// WHY THE CLAMP LIVES HERE AND NOT AT RENDER OR AT HYDRATE
//
// Opening geometry is geometry the SHELL invents; every other geometry a window
// has is geometry the USER produced (a drag, a resize, a tile) or a faithful
// re-scaling of it. Only the invented kind may be clamped:
//
//   - Clamping at RENDER (Window.tsx's style) would cage a window the user
//     deliberately dragged half off-screen — a legitimate arrangement, and one
//     the drag handler itself would then fight, because it computes the next
//     position from win.position.x while the rendered box says something else.
//   - Clamping at HYDRATE would corrupt persisted state. The writer re-
//     serializes whatever is in state, as a fraction of its own viewport, so a
//     window clamped down to fit a phone would be written back as a
//     phone-shaped fraction and come back shrunken on a 27-inch monitor
//     forever. The canonical unit's whole point is that a restore is a
//     PROPORTIONAL operation; a clamp is not proportional, so it must never
//     run on that path.
//
// So: clamp once, at open, against the viewport the window is opening on.
// MOVE_WINDOW / RESIZE_WINDOW / TILE_WINDOW stay untouched.

/** Left/right gutter kept between a freshly opened window and the screen edge.
 *  Purely so a full-width-clamped window doesn't butt the bezel; the vertical
 *  insets are MENU_BAR_H / DOCK_H, which are real occlusions rather than
 *  taste. */
export const WINDOW_EDGE_MARGIN = 8

/** The flat default every caller but Settings gets (Settings opts into a
 *  larger initial window — see ShellProvider's OPEN_WINDOW action type). Also
 *  the shape hydratePersistedState falls back to when a persisted geometry is
 *  unreadable, so the two cannot drift. */
export const DEFAULT_WINDOW_SIZE: WindowSizeLike = { width: 720, height: 500 }

/** First window's top-left, and the per-window cascade step; the cascade wraps
 *  back to the origin every OPEN_CASCADE_WRAP windows. */
export const OPEN_CASCADE_ORIGIN = { x: 60, y: 50 }
export const OPEN_CASCADE_STEP = 32
export const OPEN_CASCADE_WRAP = 6

export interface WindowSizeLike { width: number; height: number }

/** Just the extent, in the reading tab's own CSS px. Structurally satisfied by
 *  screenScale's ViewportSize (whose ViewportPx is `number & brand`), so
 *  ShellProvider can hand readViewportSize()'s result straight in. */
export interface ViewportExtent { widthPx: number; heightPx: number }

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), Math.max(lo, hi))
}

/**
 * Geometry for the `index`-th window opened on a desktop.
 *
 * @param index      how many windows the desktop already has (0 for the first)
 * @param requested  the caller's desired size, or DEFAULT_WINDOW_SIZE
 * @param viewport   the OPENING tab's own extent, or null when it cannot be
 *                   read at all (no `window`: SSR, unit tests). With null this
 *                   returns the un-clamped cascade + requested size — the exact
 *                   pre-clamp behaviour — because inventing an extent to clamp
 *                   against would be worse than not clamping.
 *
 * Fit beats every other consideration: when the viewport is too small for the
 * requested size, the size shrinks (there is no minimum that outranks being on
 * screen), and when it is too small for the cascade, the cascade collapses.
 * At 768px both happen — the window is 752 wide and every window in the cascade
 * shares x=8 — but the VERTICAL cascade survives, so a stack of windows is
 * still visibly a stack.
 */
export function openWindowGeometry(
  index: number,
  requested: WindowSizeLike,
  viewport: ViewportExtent | null,
  menuBar: number = MENU_BAR_H,
  dock: number = DOCK_H,
): TileGeometryResult {
  const step = ((index % OPEN_CASCADE_WRAP) + OPEN_CASCADE_WRAP) % OPEN_CASCADE_WRAP
  const idealX = OPEN_CASCADE_ORIGIN.x + step * OPEN_CASCADE_STEP
  const idealY = OPEN_CASCADE_ORIGIN.y + step * OPEN_CASCADE_STEP
  if (!viewport) {
    return { position: { x: idealX, y: idealY }, size: { width: requested.width, height: requested.height } }
  }
  const vw = viewport.widthPx
  const vh = viewport.heightPx
  // Size first: the position clamp depends on how wide/tall the window ended up.
  const width = Math.max(1, Math.min(requested.width, vw - 2 * WINDOW_EDGE_MARGIN))
  const height = Math.max(1, Math.min(requested.height, vh - menuBar - dock))
  const x = clamp(idealX, WINDOW_EDGE_MARGIN, vw - WINDOW_EDGE_MARGIN - width)
  const y = clamp(idealY, menuBar, vh - dock - height)
  return {
    position: { x: Math.round(x), y: Math.round(y) },
    size: { width: Math.round(width), height: Math.round(height) },
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
