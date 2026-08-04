import { useCallback, useLayoutEffect, useRef } from 'react'

// useAutoGrow — a tiny helper that makes a single-row <textarea> grow to fit its
// content up to a max height, then scroll. The composers across the shell (Home,
// Assistant) used a fixed rows={1} textarea that never expanded, so multi-line
// input got cramped into one visible line. This resizes on every value change.
//
// Usage:
//   const taRef = useAutoGrow(value, { maxHeight: 160 })
//   <textarea ref={taRef} value={value} onChange={…} />
//
// maxHeight is in px; once the content exceeds it the textarea keeps that height
// and scrolls internally (pair with a Tailwind max-h-* / overflow class).
export function useAutoGrow(value, { maxHeight = 160 } = {}) {
  const ref = useRef(null)

  const resize = useCallback(() => {
    const el = ref.current
    if (!el) return
    // Reset to auto so scrollHeight reflects the true content height (shrinking
    // as well as growing), then clamp to maxHeight.
    el.style.height = 'auto'
    const next = Math.min(el.scrollHeight, maxHeight)
    el.style.height = `${next}px`
    el.style.overflowY = el.scrollHeight > maxHeight ? 'auto' : 'hidden'
  }, [maxHeight])

  // Layout effect so the height is set before paint — no visible flash/jump.
  useLayoutEffect(() => { resize() }, [value, resize])

  return ref
}
