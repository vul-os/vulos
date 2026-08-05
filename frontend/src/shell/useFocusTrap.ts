import { useEffect, useRef, type RefObject } from 'react'

// A11Y — useFocusTrap keeps keyboard focus inside an overlay while it is open
// and restores focus to whatever was focused before it opened. Used by the
// Launchpad, Notification Center, and desktop context menu so keyboard and
// screen-reader users are not stranded outside a modal surface.
//
// Usage:
//   const ref = useFocusTrap(isOpen)
//   return <div ref={ref} role="dialog" aria-modal="true">…</div>
//
// Behaviour:
//   - on open: remembers document.activeElement, moves focus to the first
//     focusable element inside the container (or the container itself)
//   - while open: Tab / Shift+Tab cycle within the container
//   - on close: returns focus to the previously-focused element
//
// Respects elements that opt out of the trap via [data-focus-trap-ignore].
const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

export function useFocusTrap(active: boolean): RefObject<HTMLDivElement | null> {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const restoreRef = useRef<Element | null>(null)

  useEffect(() => {
    if (!active) return
    const container = containerRef.current
    if (!container) return

    // Remember where focus was so we can restore it on close.
    restoreRef.current = document.activeElement

    const focusables = (): HTMLElement[] =>
      Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        el => el.offsetParent !== null || el === document.activeElement,
      )

    // Move focus inside on the next frame (content may still be settling).
    const raf = requestAnimationFrame(() => {
      const els = focusables()
      if (els.length) els[0].focus()
      else {
        container.setAttribute('tabindex', '-1')
        container.focus()
      }
    })

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return
      const els = focusables()
      if (els.length === 0) {
        e.preventDefault()
        return
      }
      const first = els[0]
      const last = els[els.length - 1]
      const activeEl = document.activeElement
      if (e.shiftKey) {
        if (activeEl === first || !container.contains(activeEl)) {
          e.preventDefault()
          last.focus()
        }
      } else if (activeEl === last || !container.contains(activeEl)) {
        e.preventDefault()
        first.focus()
      }
    }

    container.addEventListener('keydown', onKeyDown)
    return () => {
      cancelAnimationFrame(raf)
      container.removeEventListener('keydown', onKeyDown)
      // Restore focus to the opener, if it is still in the document.
      const restore = restoreRef.current
      if (restore && typeof (restore as HTMLElement).focus === 'function' && document.contains(restore)) {
        (restore as HTMLElement).focus()
      }
    }
  }, [active])

  return containerRef
}
