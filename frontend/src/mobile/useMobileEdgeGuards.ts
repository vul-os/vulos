import { useEffect } from 'react'

// MOBILE-08 — the top edge, PWA half.
//
// Measured on the real production build at 390×844 BEFORE this hook existed:
//
//   getComputedStyle(document.documentElement).overscrollBehaviorY === 'auto'
//   getComputedStyle(document.body).overscrollBehaviorY            === 'auto'
//
// which means Chrome's **pull-to-refresh is armed on the whole OS shell**. A
// downward drag near the top of any Vulos surface reloads the entire shell —
// every open app's in-memory state, every iframe, the assistant conversation,
// gone. In an installed PWA there is no tab strip and no visible URL bar, so the
// user gets no warning and no way to interpret what just happened: the OS simply
// blinked. That is strictly worse than the notification-shade problem the founder
// asked about, and unlike the shade it is entirely ours to fix.
//
// `overscroll-behavior-y: contain` on the ROOT SCROLLER disables pull-to-refresh
// while leaving normal scrolling and scroll chaining inside the page alone. It
// must be on `html`/`body` — Chrome reads the property from the root scroller,
// so putting it on `.vmob-root` (which is `position: fixed` and never the root
// scroller) does nothing. That is why this is a hook that stamps the document
// element rather than a rule on a component.
//
// It is applied as a CLASS, not an inline style, and removed on unmount, so:
//   - the desktop shell is untouched (MobileStack is the only caller, and it is
//     only mounted on the mobile layout), and
//   - src/index.css — which this workstream does not own — needs no change.
//
// WHAT THIS DOES NOT DO: it does not, and cannot, stop the Android notification
// shade. See roadmap/MOBILE-SHELL.md §4 — no web API can. This kills the one
// top-edge gesture a web page is permitted to own, and the design work is to
// make sure Vulos never puts anything the user needs at the top edge.
export const MOBILE_ROOT_CLASS = 'vmob-active'

export function useMobileEdgeGuards(): void {
  useEffect(() => {
    const root = document.documentElement
    root.classList.add(MOBILE_ROOT_CLASS)
    return () => { root.classList.remove(MOBILE_ROOT_CLASS) }
  }, [])
}
