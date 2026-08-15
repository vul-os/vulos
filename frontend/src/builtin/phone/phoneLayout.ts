// phoneLayout.ts — size awareness and the tab set.
//
// Split out of PhoneChrome.tsx because a module that exports both components and
// non-components breaks Fast Refresh (react-refresh/only-export-components):
// editing a constant here would otherwise force a full reload and drop the app's
// state, which for a dial pad means losing the number you were typing.

import { useState, useEffect, useRef } from 'react'

export type Size = 'narrow' | 'medium' | 'wide'

/**
 * Breakpoints come from the ELEMENT, not the viewport.
 *
 * This app runs in a desktop window whose width the user drags, inside a tablet
 * layout, and full-bleed on a phone. A CSS media query only ever sees the
 * viewport, so a 380px-wide window on a 2560px monitor would render the desktop
 * two-pane layout squeezed into a rail — exactly the class of defect that
 * survives a unit suite and only shows up when you look at it. A ResizeObserver
 * on our own box is the only thing that knows the real width.
 */
export function useSize(): [Size, (el: HTMLDivElement | null) => void] {
  const [size, setSize] = useState<Size>('medium')
  const obs = useRef<ResizeObserver | null>(null)

  useEffect(() => () => obs.current?.disconnect(), [])

  const ref = (el: HTMLDivElement | null) => {
    obs.current?.disconnect()
    if (!el) return
    const apply = (w: number) => setSize(w < 560 ? 'narrow' : w < 900 ? 'medium' : 'wide')
    apply(el.getBoundingClientRect().width)
    if (typeof ResizeObserver === 'undefined') return
    obs.current = new ResizeObserver((entries) => {
      for (const e of entries) apply(e.contentRect.width)
    })
    obs.current.observe(el)
  }

  return [size, ref]
}

export type TabId = 'recents' | 'contacts' | 'keypad' | 'messages'

export const TABS: { id: TabId; label: string; glyph: string }[] = [
  { id: 'recents', label: 'Recents', glyph: '🕘' },
  { id: 'contacts', label: 'Contacts', glyph: '👤' },
  { id: 'keypad', label: 'Keypad', glyph: '⌨' },
  { id: 'messages', label: 'Messages', glyph: '💬' },
]
