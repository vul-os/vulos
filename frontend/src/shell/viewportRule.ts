/**
 * The viewport rule, in a module that imports nothing.
 *
 * This lived in useViewport.ts, and desktop/store.ts imported it from there —
 * which broke two suites at IMPORT time. store.ts calls initDesktopLayout() at
 * module scope, so it invokes this during module evaluation; any test that
 * mocks '../shell/useViewport' without re-exporting this one function left the
 * binding undefined and the file failed to collect. 1110 tests still passed,
 * because the two files never ran at all.
 *
 * A leaf with no imports cannot be caught by that: mocking the hook no longer
 * touches the rule, and the rule cannot participate in a cycle. Both callers
 * still get ONE implementation, which was the point — the two used to agree by
 * comment and had already drifted.
 */

export const MOBILE_BREAKPOINT = 768

// MOBILE-07 — a touch tablet is not a small desktop.
//
// The shell picks its layout in App.tsx as
//   useDesktop = (deviceProfile === 'pc' || 'desktop') && layout === 'desktop'
// and `deviceProfile` comes from GET /api/device-profile — a **box-side**
// setting that defaults to 'pc'. The box is a PC; the phone or tablet rendering
// it over Relay is not (see clients/android/DECISIONS.md MOB-01: the client is a
// thin client, the box is the authority). So the device profile says nothing
// about the device a human is actually touching, and every real deployment
// leaves it at 'pc'. That made width the only thing standing between a touch
// tablet and the full desktop canvas — and at 768×1024 or 834×1194 width alone
// says "desktop".
//
// Measured on a 768×1024 touch profile before this rule existed: the shell
// rendered DesktopCanvas, whose window controls are **12×12 px** (`.vwin-light`)
// and whose widget rail button is 27 px tall. Nothing about that is operable
// with a thumb, and no amount of work inside MobileStack could reach it —
// MobileStack was never mounted.
//
// So the predicate gains a second, narrow clause: a viewport up to
// TOUCH_STACK_MAX that reports a **coarse pointer with no hover** is treated as
// mobile. Both halves matter:
//   - `pointer: coarse`  — the primary input is a finger, not a mouse.
//   - `hover: none`      — there is no hovering pointer at all. A touchscreen
//                          laptop reports `hover: hover` because its mouse is
//                          the primary pointer, so it is NOT caught by this and
//                          keeps the desktop canvas. This is the standard test
//                          and it is what keeps the blast radius to real
//                          tablets.
// Width still leads: below MOBILE_BREAKPOINT everything is mobile regardless of
// pointer, exactly as before, so no desktop path changes.
//
// TOUCH_STACK_MAX is 1024 — an iPad-class tablet in PORTRAIT (768, 834, 1024
// logical px) gets the touch shell; the same tablet in landscape (1180/1366) has
// the room to run the canvas, and is a keyboard-and-trackpad posture often
// enough that forcing a single column there would be the worse error. That line
// is a judgement call, recorded here so it can be moved deliberately.
export const TOUCH_STACK_MAX = 1024
const TOUCH_QUERY = `(max-width: ${TOUCH_STACK_MAX}px) and (pointer: coarse) and (hover: none)`

export type ViewportLayout = 'mobile' | 'desktop'

// Exported for the unit test, which drives the two queries independently — a
// single combined string would let a mutation to either half pass unnoticed.
export const VIEWPORT_QUERIES = {
  narrow: `(max-width: ${MOBILE_BREAKPOINT - 1}px)`,
  touchTablet: TOUCH_QUERY,
}

/**
 * The single answer to "is the touch shell up right now".
 *
 * Exported because `desktop/store.ts` needs the same answer to pick a dock
 * profile, and it used to compute its own — `innerWidth < 768`, with a comment
 * claiming it mirrored this file exactly. It did not: this file ALSO treats a
 * coarse-pointer tablet up to 1024px as mobile, so between 768 and 1024 the
 * touch shell was up while the layout store said `desktop`. An iPad in portrait
 * got the desktop dock's twelve-item, small-tile geometry inside the mobile
 * shell.
 *
 * Two rules that agree by comment drift the moment one of them is edited, and
 * this one had already drifted. One function, imported by both.
 */
export function resolveViewportLayout(): ViewportLayout {
  if (typeof window === 'undefined') return 'desktop'
  if (window.innerWidth < MOBILE_BREAKPOINT) return 'mobile'
  if (window.matchMedia?.(TOUCH_QUERY).matches) return 'mobile'
  return 'desktop'
}

