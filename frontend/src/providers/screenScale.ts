// screenScale.ts — the canonical unit for pixel-denominated values that cross
// shared shell state between browser instances.
//
// # What reaches the shell about output scale today (there was nothing)
//
// Multi-screen runs one browser per physical output (screenIdentity.ts,
// roadmap/SCREENS.md). `screen=` / `screens=` / `screenIndex=` — the only
// parameters the kiosk launcher passes — carry a connector NAME and an
// ORDINAL. Nothing about resolution, scale or device pixel ratio crosses that
// boundary, and it doesn't need to: each browser instance already knows its
// own `window.innerWidth/innerHeight` and `window.devicePixelRatio` simply by
// being the thing rendering on that output. The launcher enumerating that and
// re-injecting it into the URL would be redundant with what the browser
// already has, and could go stale as soon as the compositor changed a scale
// factor at runtime, which the URL cannot.
//
// So the missing piece was never "get scale into the shell" — it was that
// nothing stopped a pixel value SHAPED BY one instance's viewport from being
// written into state another instance also reads. Grep confirms the shared
// path: `ShellProvider.tsx`'s `serializableShellState` copies each window's
// `position`/`size` verbatim (raw CSS px, e.g. `w.position = { x: 1800, y:
// 40 }`) into the object passed to `useShellSession`'s `publish`, which
// `ch.postMessage({ kind: 'state', ... })`s it to every follower tab
// (`useShellSession.ts`), and a follower applies it straight back with
// `dispatch({ type: 'RESTORE_STATE', saved: session.mirrored })`
// (`ShellProvider.tsx`, the `session.mirrored` effect). shellSession.ts's own
// comments say watching one desktop from two tabs — now two SCREENS — is a
// supported arrangement, not an edge case.
//
// # The hazard, worked through concretely
//
// A window dragged to CSS-px (1800, 40) on a 1920×1080 writer viewport is
// almost at the right edge. Broadcast unconverted to a follower on a
// 3840×2160 4K viewport (whether that viewport got there via DPR=1 native
// pixels or a compositor scale that doesn't match the writer's), it lands
// less than halfway across — nowhere near where it was dragged. The reverse
// direction is worse: a position meaningful on the 4K side can be entirely
// off-screen on a smaller follower. Device pixel ratio alone doesn't predict
// this (CSS px are already DPR-normalized *for the screen that measured
// them* — that is what CSS px means), so re-dividing by DPR a second time
// would double-correct. What actually varies between instances is each
// viewport's own CSS-pixel EXTENT (innerWidth/innerHeight), which is a
// product of the output's physical resolution and whatever scale the
// compositor gave it. That extent is what a shared pixel value must be
// converted through.
//
// # The canonical unit: a fraction of the viewport that measured it
//
// Not raw CSS px (viewport-dependent, per above). Not "px at some agreed
// reference resolution" — that needs every peer to know and honour a shared
// constant, and a peer that doesn't breaks the same way raw px does. A
// fraction of the writer's OWN viewport needs no negotiation: the writer
// divides by its own extent before it leaves this process, a follower
// multiplies by ITS OWN extent on receipt, and neither needs to know
// anything about the other's resolution, DPR, or scale. Two screens showing
// the same desktop each see a window in the same RELATIVE place — centered
// stays centered, near-the-right-edge stays near-the-right-edge — which is
// the only thing "the same window, mirrored to another screen" can mean once
// the two screens don't share a pixel grid.
//
// # The boundary is a branded type, not a comment
//
// A raw `number` is indistinguishable from a canonical fraction at the type
// level, so nothing would stop a future edit from writing a viewport-px
// value straight into a field meant to hold one. `CanonicalUnit` and
// `ViewportPx` below are nominally typed (impossible to construct one from a
// bare `number` without going through the functions in this file), so
// passing viewport px where shared state expects a canonical fraction — or
// vice versa — is a `npm run typecheck` failure, not a bug that only shows up
// on a desk with two different monitors. `isPlausibleCanonicalUnit` backs
// that up at runtime, where branding erases: a raw px value fed in where a
// fraction was expected is almost always wildly out of a fraction's normal
// range and gets caught rather than silently mis-scaling a window.

/**
 * A value already normalized to a fraction of the viewport that measured it —
 * 0.5 means "halfway across/down THAT viewport". Safe to persist to shared
 * shell state (`serializableShellState`, `useShellSession.publish`) because it
 * carries no assumption about which screen reads it back.
 */
export type CanonicalUnit = number & { readonly __brand: 'CanonicalUnit' }

/**
 * A value denominated in THIS browser instance's own CSS pixels — what
 * `WindowPosition`/`WindowSize` hold today. Never write one of these
 * directly into a `publish`ed or `saveShellState`d value that another screen
 * may read; convert with `toCanonical`/`toCanonicalPoint`/`toCanonicalSize`
 * first.
 */
export type ViewportPx = number & { readonly __brand: 'ViewportPx' }

/** Brands a plain number as viewport px. The only place a `ViewportPx` should be minted from a literal is a test; real values come from `readViewportSize()`. */
export function viewportPx(n: number): ViewportPx {
  return n as ViewportPx
}

export interface ViewportSize {
  widthPx: ViewportPx
  heightPx: ViewportPx
}

/**
 * readViewportSize reads the CURRENT document's own viewport in CSS px.
 *
 * Guarded like `readScreenIdentity` in screenIdentity.ts: the shell also
 * renders outside a browser (tests, any future SSR), where `window` does not
 * exist. Also refuses a zero or negative extent, which would make every
 * fraction computed against it either `Infinity`/`NaN` or division-by-zero —
 * a 0×0 viewport is not a real screen a window can be placed relative to.
 */
export function readViewportSize(): ViewportSize | null {
  const win = globalThis as { innerWidth?: number; innerHeight?: number }
  if (typeof win.innerWidth !== 'number' || typeof win.innerHeight !== 'number') return null
  if (!(win.innerWidth > 0) || !(win.innerHeight > 0)) return null
  return { widthPx: viewportPx(win.innerWidth), heightPx: viewportPx(win.innerHeight) }
}

/**
 * readDevicePixelRatio exists for diagnostics/telemetry only — it is
 * deliberately NOT part of the conversion path. See the module comment:
 * CSS px are already normalized for the reporting screen's own DPR, so
 * dividing by DPR again would double-correct. What differs between two
 * instances is viewport EXTENT (`readViewportSize`), not DPR by itself; two
 * screens can share a DPR and still need conversion (different resolutions
 * scaled the same), or differ in DPR and need NONE (a 4K screen scaled 2x
 * sits at the same CSS-px extent as an unscaled 1080p one beside it).
 */
export function readDevicePixelRatio(): number | null {
  const win = globalThis as { devicePixelRatio?: number }
  return typeof win.devicePixelRatio === 'number' && win.devicePixelRatio > 0 ? win.devicePixelRatio : null
}

/** Converts a viewport-px value to the canonical fraction of the given extent. */
export function toCanonical(px: ViewportPx, extentPx: ViewportPx): CanonicalUnit {
  return (extentPx === 0 ? 0 : (px as number) / (extentPx as number)) as CanonicalUnit
}

/** Converts a canonical fraction back to viewport px for the given extent — the extent of the READING viewport, not the one that produced the fraction. */
export function fromCanonical(unit: CanonicalUnit, extentPx: ViewportPx): ViewportPx {
  return ((unit as number) * (extentPx as number)) as ViewportPx
}

/**
 * isPlausibleCanonicalUnit is the runtime backstop for the branding above.
 * Branding is erased at runtime (it's still just a `number`), so a value that
 * skipped `toCanonical` — e.g. a raw viewport-px value assigned where a
 * fraction was expected — would otherwise pass through silently. A real
 * fraction of a viewport very rarely leaves roughly [-4, 5]: a window can
 * hang partway off any edge, but a raw CSS-px number (routinely in the
 * hundreds or thousands) is not going to land in that band by accident.
 * Meant for a call at the shared-state boundary (see the report to
 * ShellProvider.tsx's owner), not for hot paths.
 */
export function isPlausibleCanonicalUnit(n: number): boolean {
  return Number.isFinite(n) && Math.abs(n) < 5
}

export interface PixelPoint { x: ViewportPx; y: ViewportPx }
export interface CanonicalPoint { nx: CanonicalUnit; ny: CanonicalUnit }

export function toCanonicalPoint(p: PixelPoint, viewport: ViewportSize): CanonicalPoint {
  return { nx: toCanonical(p.x, viewport.widthPx), ny: toCanonical(p.y, viewport.heightPx) }
}

export function fromCanonicalPoint(p: CanonicalPoint, viewport: ViewportSize): PixelPoint {
  return { x: fromCanonical(p.nx, viewport.widthPx), y: fromCanonical(p.ny, viewport.heightPx) }
}

export interface PixelSize { width: ViewportPx; height: ViewportPx }
export interface CanonicalSize { nw: CanonicalUnit; nh: CanonicalUnit }

export function toCanonicalSize(s: PixelSize, viewport: ViewportSize): CanonicalSize {
  return { nw: toCanonical(s.width, viewport.widthPx), nh: toCanonical(s.height, viewport.heightPx) }
}

export function fromCanonicalSize(s: CanonicalSize, viewport: ViewportSize): PixelSize {
  return { width: fromCanonical(s.nw, viewport.widthPx), height: fromCanonical(s.nh, viewport.heightPx) }
}
