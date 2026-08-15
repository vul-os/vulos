import { useState, useEffect } from 'react'

const MOBILE_BREAKPOINT = 768

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
const TOUCH_STACK_MAX = 1024
const TOUCH_QUERY = `(max-width: ${TOUCH_STACK_MAX}px) and (pointer: coarse) and (hover: none)`

export type ViewportLayout = 'mobile' | 'desktop'

// Exported for the unit test, which drives the two queries independently — a
// single combined string would let a mutation to either half pass unnoticed.
export const VIEWPORT_QUERIES = {
  narrow: `(max-width: ${MOBILE_BREAKPOINT - 1}px)`,
  touchTablet: TOUCH_QUERY,
}

function resolve(): ViewportLayout {
  if (typeof window === 'undefined') return 'desktop'
  if (window.innerWidth < MOBILE_BREAKPOINT) return 'mobile'
  if (window.matchMedia?.(TOUCH_QUERY).matches) return 'mobile'
  return 'desktop'
}

export function useViewport(): ViewportLayout {
  const [layout, setLayout] = useState<ViewportLayout>(resolve)

  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return undefined
    // Both queries have to be watched. Rotating a tablet from portrait to
    // landscape crosses TOUCH_STACK_MAX without crossing MOBILE_BREAKPOINT, so a
    // listener on the narrow query alone would leave the shell in the wrong
    // layout until something else forced a re-render.
    const narrow = window.matchMedia(VIEWPORT_QUERIES.narrow)
    const touch = window.matchMedia(VIEWPORT_QUERIES.touchTablet)
    const on = () => setLayout(resolve())
    narrow.addEventListener('change', on)
    touch.addEventListener('change', on)
    return () => {
      narrow.removeEventListener('change', on)
      touch.removeEventListener('change', on)
    }
  }, [])

  return layout
}
