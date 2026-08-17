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

export interface Tab { id: TabId; label: string; glyph: string }

/**
 * CONTACTS IS FIRST, AND IS THE DEFAULT.
 *
 * The founder's ask, in his words: calls should "live with contacts,
 * Android-style — one surface where you see people, call them, and see your
 * call history", not a phone widget with an address book bolted onto it. So the
 * surface opens on PEOPLE. Recents is the second page, not the front page:
 * you reach for this app to call someone far more often than to audit a log.
 */
export const TABS: Tab[] = [
  { id: 'contacts', label: 'Contacts', glyph: '👤' },
  { id: 'recents', label: 'Recents', glyph: '🕘' },
  { id: 'keypad', label: 'Keypad', glyph: '⌨' },
  { id: 'messages', label: 'Messages', glyph: '💬' },
]

export const DEFAULT_TAB: TabId = 'contacts'

/**
 * Which pages exist on THIS box.
 *
 * Most Vulos boxes have no modem, and on those a dial pad and an SMS inbox are
 * pages that can only ever be empty and can only ever fail — so they are not
 * offered at all. The address book is not hardware-dependent and is always
 * there; Recents is always there too, because it is where a box with no radio
 * gets told what to plug in, and because Vulos-to-Vulos call history comes from
 * a different service that has nothing to do with GSM.
 *
 * `hasLine` is "some line exists", NOT "some line can place calls": a data/SMS
 * -only modem still has an inbox and can still send, and hiding Messages from
 * it would remove a working feature over an unrelated missing capability.
 */
export function visibleTabs(hasLine: boolean): Tab[] {
  return hasLine ? TABS : TABS.filter((t) => t.id === 'contacts' || t.id === 'recents')
}

/**
 * A fill for small white text.
 *
 * `--accent-contrast` (white) on a raw `--accent` fill measures 3.68:1 with this
 * theme's default accent — under the 4.5:1 floor for the unread count and the
 * outgoing message bubble, in BOTH themes, because a fill carries its own pair
 * and the surface underneath never enters into it. Darkening the fill is the
 * half that can be fixed here; `--accent` itself is shared, user-customisable
 * and not this app's to redefine.
 */
export const ACCENT_FILL = 'color-mix(in srgb, var(--accent) 76%, #000)'
