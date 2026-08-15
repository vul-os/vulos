import { useState, useEffect } from 'react'

import {
  MOBILE_BREAKPOINT, VIEWPORT_QUERIES, resolveViewportLayout, type ViewportLayout,
} from './viewportRule'

// Re-exported so existing importers are unaffected by the move.
export { MOBILE_BREAKPOINT, VIEWPORT_QUERIES, resolveViewportLayout }
export type { ViewportLayout }

const resolve = resolveViewportLayout

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
