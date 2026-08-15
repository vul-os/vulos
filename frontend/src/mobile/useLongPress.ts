import { useCallback, useEffect, useMemo, useRef } from 'react'

/**
 * Long press — the touch idiom for "the actions that are not the main one".
 *
 * # Why this exists
 *
 * A sweep of `src/` this session found NO long-press handler anywhere in the
 * tree, and the cost of that landed hardest in the file manager: right-click was
 * the only route to "Ask AI about this" and "Share to peer…", so on a phone
 * those two actions did not exist. They were not hidden or awkward — there was
 * no gesture that could reach them.
 *
 * A second button next to every row would have "fixed" it. It would also have
 * added a permanent control to the desktop, where right-click is correct and
 * already works, and it would have to be repeated in every list that ever grows
 * a context menu. Long press is what every touch platform already means by
 * "more actions on this thing", it costs no pixels, and it is the same gesture
 * users arrive with.
 *
 * # What this is careful about
 *
 * 1. TOUCH ONLY. It fires for `pointerType` of `touch` or `pen` and never for a
 *    mouse. A mouse user pressing and holding is not asking for anything, and a
 *    long press that fired on a mouse would race the real `contextmenu`.
 *
 * 2. NO DOUBLE MENU. Chrome on Android fires its own `contextmenu` after a long
 *    press; iOS Safari does not. Handling only `contextmenu` would therefore
 *    have worked on one of the two platforms Vulos ships to — which is the
 *    shape of defect that survives a review on a laptop. This runs its own timer
 *    and reports, via `suppressContextMenu()`, that the native event which
 *    follows must be swallowed rather than opening a second menu on top of the
 *    first.
 *
 * 3. MOVEMENT CANCELS. A finger that travels more than `slop` px is scrolling.
 *    Without this, every flick down a long list would fire a long press on
 *    whatever row it started on — the single most common way a hand-rolled long
 *    press turns a scrollable list into a minefield.
 *
 * 4. IT DOES NOT SWALLOW THE TAP. Firing the long press marks the gesture as
 *    consumed; `consumed()` lets the click handler that follows bail out, so a
 *    press that opened the menu does not also select or navigate.
 *
 * # Where it lives
 *
 * In `src/mobile/`, which is where this shell keeps its touch primitives, and it
 * is deliberately generic: it takes an element's pointer props and knows nothing
 * about files, rows or menus. `builtin/files/FileManager.tsx` is its first
 * caller; anything else that grows a context menu should be its second rather
 * than growing a second implementation.
 */

/** Milliseconds a finger must rest before the press counts. */
export const LONG_PRESS_MS = 500

/**
 * How far a finger may travel first, in CSS px.
 *
 * 10px is the conventional touch slop. Below ~8px a resting thumb's own tremor
 * cancels the gesture on a high-DPI screen; above ~14px a deliberate short drag
 * starts registering as a press.
 */
export const LONG_PRESS_SLOP = 10

export interface LongPressPoint {
  x: number
  y: number
}

export interface UseLongPressOptions {
  /** Called with the viewport point the finger rested on. */
  onLongPress: (point: LongPressPoint) => void
  ms?: number
  slop?: number
  disabled?: boolean
}

/** The pointer props to spread onto the element, plus the gesture's state. */
export interface LongPressHandlers {
  onPointerDown: (e: React.PointerEvent) => void
  onPointerMove: (e: React.PointerEvent) => void
  onPointerUp: (e: React.PointerEvent) => void
  onPointerCancel: (e: React.PointerEvent) => void
  /**
   * True from the moment a long press fires until the next pointerdown. The
   * click that a touch always synthesises afterwards reads this and does
   * nothing, so opening the menu never also selects or navigates.
   */
  consumed: () => boolean
  /**
   * True when a native `contextmenu` arriving now is the platform's echo of a
   * long press this hook already handled (Chrome on Android). The caller
   * preventDefaults it and returns instead of opening a second menu.
   */
  suppressContextMenu: () => boolean
}

/** Whether a pointer event came from a finger or a stylus rather than a mouse. */
export function isTouchPointer(e: { pointerType?: string }): boolean {
  return e.pointerType === 'touch' || e.pointerType === 'pen'
}

export interface PointerKind {
  /** Spread onto `onPointerDown`. Records what the coming click came from. */
  track: (e: React.PointerEvent) => void
  /** Whether the gesture in progress started with a finger or a stylus. */
  wasTouch: () => boolean
}

/**
 * Which kind of pointer produced the click that is about to arrive.
 *
 * React's synthetic click carries no `pointerType`, and a media query is the
 * wrong instrument: `(pointer: coarse)` describes the DEVICE, so on a
 * touchscreen laptop it would answer for the mouse the user is actually holding.
 * The pointerdown that precedes every click knows exactly, so it is recorded
 * there and read in the click.
 */
export function usePointerKind(): PointerKind {
  const touch = useRef(false)
  return useMemo(() => ({
    track: (e: React.PointerEvent) => { touch.current = isTouchPointer(e) },
    wasTouch: () => touch.current,
  }), [])
}

export function useLongPress({
  onLongPress,
  ms = LONG_PRESS_MS,
  slop = LONG_PRESS_SLOP,
  disabled = false,
}: UseLongPressOptions): LongPressHandlers {
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const origin = useRef<LongPressPoint | null>(null)
  const fired = useRef(false)

  const clear = useCallback(() => {
    if (timer.current !== null) {
      clearTimeout(timer.current)
      timer.current = null
    }
    origin.current = null
  }, [])

  // A pending timer must not outlive the component: a row unmounted mid-press
  // (a directory that finished loading under the finger) would otherwise open a
  // menu for a file that is no longer on screen.
  useEffect(() => clear, [clear])

  return useMemo(() => ({
    onPointerDown(e: React.PointerEvent) {
      fired.current = false
      clear()
      if (disabled || !isTouchPointer(e)) return
      const point = { x: e.clientX, y: e.clientY }
      origin.current = point
      timer.current = setTimeout(() => {
        timer.current = null
        // Only fire if the finger is still where it started. The move handler
        // clears `origin` the moment it travels, so this is also the guard for
        // a scroll that began under the threshold.
        if (!origin.current) return
        fired.current = true
        onLongPress(point)
      }, ms)
    },
    onPointerMove(e: React.PointerEvent) {
      const start = origin.current
      if (!start || timer.current === null) return
      if (Math.abs(e.clientX - start.x) > slop || Math.abs(e.clientY - start.y) > slop) clear()
    },
    onPointerUp() { clear() },
    onPointerCancel() { clear() },
    consumed: () => fired.current,
    suppressContextMenu: () => fired.current,
    // `onLongPress` is a dependency rather than a ref written during render:
    // writing a ref in the render body is a React rule violation the lint rules
    // catch, and the callback the timer needs is the one from the render the
    // press STARTED in — which is exactly what a closed-over dependency gives.
  }), [clear, disabled, ms, slop, onLongPress])
}
