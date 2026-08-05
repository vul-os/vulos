import { useState, useEffect } from 'react'

// useNarrow — a shared viewport-width predicate for the builtins' MOBILE-ADAPTIVE
// layouts (WAVE-30). Two-pane apps (Contacts, Files, Messages) use it to collapse
// a fixed sidebar into a single stacked pane on phones, rather than shrinking the
// desktop layout into an unusable sliver.
//
// It is viewport-based (matchMedia), matching Tailwind's responsive prefixes, so
// a builtin rendered in a narrow *desktop window* keeps its two-pane layout (the
// window is resizable) while a builtin rendered FULLSCREEN on a phone collapses.
//
// Default breakpoint 640px === Tailwind's `sm`.
export function useNarrow(maxWidth: number = 640): boolean {
  const query = `(max-width: ${maxWidth}px)`
  const [narrow, setNarrow] = useState(
    () => typeof window !== 'undefined' && !!window.matchMedia?.(query).matches
  )
  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return undefined
    const mq = window.matchMedia(query)
    const on = (e: MediaQueryListEvent) => setNarrow(e.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [query])
  return narrow
}
