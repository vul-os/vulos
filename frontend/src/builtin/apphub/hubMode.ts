import { useState, useLayoutEffect } from 'react'

/**
 * The hub's structural layout, chosen from the HUB's OWN width.
 *
 * Not the viewport's. The hub is a window the user drags, so on a 1440px screen
 * it is routinely 400px wide; every `md:` / `xl:` class in the previous version
 * was therefore answering a question nobody asked. Measured before this changed:
 * a 442px window on a 1440px screen rendered a three-column grid, 107px of it
 * outside the window frame, with every app name clipped to one letter.
 *
 * This lives in its own module rather than beside the component because a file
 * that exports both a component and a plain function breaks React Fast Refresh
 * (react-refresh/only-export-components) — the whole module gets remounted on
 * every edit instead of hot-swapped.
 */
export type HubMode = 'narrow' | 'compact' | 'mid' | 'wide'

/**
 * Thresholds are stated once, here, and consumed by both the component and the
 * specs — so a spec can say which layout it is exercising instead of
 * rediscovering the boundary from pixels.
 *
 * A guard, not decoration: `hubModeFor` is called with `getBoundingClientRect().width`,
 * which is 0 for an element that is display:none or not yet laid out. Zero must
 * resolve to the SAFEST layout (narrow, single column) rather than the widest —
 * defaulting a not-yet-measured hub to `wide` is how a phone gets one frame of
 * three-column grid before the observer corrects it.
 */
export function hubModeFor(width: number): HubMode {
  if (width >= 1080) return 'wide'
  if (width >= 700) return 'mid'
  if (width >= 440) return 'compact'
  return 'narrow'
}

/**
 * Track the hub's own width.
 *
 * Falls back to `wide` where ResizeObserver does not exist (jsdom), which keeps
 * the unit tests on one deterministic layout — in a real browser the observer
 * fires before paint, so the fallback is never what a user sees.
 */
export function useHubMode(ref: React.RefObject<HTMLDivElement | null>): HubMode {
  const [mode, setMode] = useState<HubMode>('wide')
  useLayoutEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return undefined
    const apply = () => setMode(hubModeFor(el.getBoundingClientRect().width))
    apply()
    const ro = new ResizeObserver(apply)
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref])
  return mode
}
