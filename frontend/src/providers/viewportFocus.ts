// viewportFocus.ts — whether THIS browser instance currently holds the
// compositor's input focus.
//
// roadmap/SCREENS.md, "Input focus across instances": several browser
// instances share one session (see the design note's (b) model — one
// instance per output), and only one of them is ever the target of a real
// keypress at a time. The compositor decides which; the shell's job is to
// FOLLOW that decision, never compete with it. This module is the read-only
// primitive for doing so — nothing here ever calls `.focus()` or otherwise
// tries to acquire focus. A shell that requested focus would itself be the
// thing competing with the compositor, which is the exact failure mode this
// exists to avoid.
//
// document.hasFocus() (and the window 'focus'/'blur' events useViewportFocus
// subscribes to) report whether THIS document's top-level browsing context
// currently has focus. For a real multi-output wlroots/labwc kiosk setup
// (roadmap/SCREENS.md) that is set by the compositor routing input to one
// output's window and not the others — which is also, mechanically, why an
// unfocused instance's `window` never receives a real keydown DOM event at
// all: the browser simply doesn't dispatch one there. That native behaviour
// already makes "a keystroke acts once" true for genuine physical key
// presses without this module's help. What this module is FOR is turning
// that implicit reliance into something explicit, queryable and testable —
// so code that currently has no focus awareness at all (nothing in this
// codebase calls document.hasFocus() anywhere today, checked by grep before
// writing this) can defend deliberately rather than by accident, and so a
// future change that relays input across instances (there is already a
// stubbed, unused `intent` SessionMessage kind in shellSession.ts for
// exactly that) has a primitive to check against instead of reinventing one
// under deadline.
//
// UNVERIFIED ON REAL HARDWARE, same as the rest of this area: whether labwc
// actually updates document.hasFocus() correctly when it moves focus between
// two SEPARATE browser processes on two SEPARATE outputs has never been
// observed on this project — every other multi-screen claim in
// roadmap/SCREENS.md carries the identical caveat, for the identical reason
// (no two-monitor rig in CI). What IS certain, and what this module's own
// tests rest on, is document.hasFocus()'s spec'd single-document behaviour,
// which jsdom implements. Two SEPARATE documents — i.e. two real browser
// instances — is exactly the part that cannot be reproduced in this test
// suite: vitest's jsdom environment hands every test ONE shared global
// `document`, so two things reading `document.hasFocus()` in the same test
// are always reading the SAME underlying state, never two independently
// focused windows. hasViewportFocus takes an injectable host specifically so
// its tests can model two instances' independent focus state anyway (see
// its own doc comment) — that proves the BRANCHING LOGIC for two instances,
// which is what code in this repo can act on; it does not and cannot prove
// that a real compositor drives two real `document.hasFocus()` values apart
// the way this assumes. That gap stays open until it can be checked against
// actual hardware.

import { useEffect, useState } from 'react'

/** The minimal shape hasViewportFocus needs from a window-like object —
 *  narrow on purpose so a test can hand it a plain fake instead of a real
 *  Window. */
interface FocusHost {
  document?: { hasFocus?: () => boolean }
}

/**
 * hasViewportFocus reports whether a window-like object's document currently
 * has focus. Defaults to `globalThis` (this instance, for real use); a test
 * supplies a fake host to model a DIFFERENT hypothetical instance's focus
 * state, which is how this module's tests exercise "two instances, only one
 * focused" without two real documents — see the module comment for why that
 * distinction matters and what it does and doesn't prove.
 *
 * Permissive default (true) when there is no document to ask (SSR, a
 * non-browser embedding, or a host that doesn't implement hasFocus) —
 * consistent with this codebase's existing rule that the absence of
 * cross-instance machinery means "behave as the single, owning instance"
 * (see shellSession.ts's openChannel returning null for the same reason).
 */
export function hasViewportFocus(win: FocusHost = globalThis as unknown as FocusHost): boolean {
  const doc = win.document
  if (!doc || typeof doc.hasFocus !== 'function') return true
  try {
    return doc.hasFocus()
  } catch {
    // A host whose hasFocus() throws is not a signal to trust either way;
    // permissive default, same as "no document at all".
    return true
  }
}

/**
 * useViewportFocus reactively tracks THIS instance's own focus state via the
 * real window 'focus'/'blur' events, initialised from hasViewportFocus().
 * Read-only — see the module comment for why acquiring focus would defeat
 * the point.
 */
export function useViewportFocus(): boolean {
  const [focused, setFocused] = useState(() => hasViewportFocus())

  useEffect(() => {
    if (typeof window === 'undefined') return
    const onFocus = () => setFocused(true)
    const onBlur = () => setFocused(false)
    window.addEventListener('focus', onFocus)
    window.addEventListener('blur', onBlur)
    return () => {
      window.removeEventListener('focus', onFocus)
      window.removeEventListener('blur', onBlur)
    }
  }, [])

  return focused
}
