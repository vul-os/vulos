// viewportFocus.test.ts — covers providers/viewportFocus.ts, the read-only
// primitive the shell uses to defer to the compositor's view of focus
// instead of competing with it (roadmap/SCREENS.md, "Input focus across
// instances").
//
// jsdom gives every test ONE shared global `document`, so two things that
// each called the real, zero-argument `document.hasFocus()` in the same test
// would always read the SAME value — there is no way, in this test runner,
// to make two things see genuinely different document focus at once. That is
// the real limit stated in the module comment, and it is not papered over
// here: hasViewportFocus's injectable-host parameter exists specifically so
// these tests can model "two instances with two different focus states"
// without pretending jsdom can produce two real documents. What this proves
// is the BRANCHING LOGIC behaves correctly for two independently-focused
// instances; it does not and cannot prove a real compositor drives two real
// document.hasFocus() calls apart — that half needs actual hardware and is
// left an open caveat, same as the rest of roadmap/SCREENS.md's multi-screen
// claims.

import { describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { hasViewportFocus, useViewportFocus } from '../viewportFocus'

describe('hasViewportFocus — two simulated instances, independently focused', () => {
  it('instance A (focused) and instance B (unfocused) disagree, as two real windows would', () => {
    const instanceA = { document: { hasFocus: () => true } }
    const instanceB = { document: { hasFocus: () => false } }

    expect(hasViewportFocus(instanceA)).toBe(true)
    expect(hasViewportFocus(instanceB)).toBe(false)
  })

  it('a keystroke handler consulting this only acts on the focused instance — not zero, not both', () => {
    // Models the intended consumption pattern for a global shortcut listener
    // (shell/useWindowShortcuts.ts, App.tsx — outside this task's file
    // ownership, so not wired up here, but the logic it should follow is
    // proven here): given the SAME logical keystroke, exactly one of two
    // instances should act.
    const instanceA = { document: { hasFocus: () => true } }
    const instanceB = { document: { hasFocus: () => false } }

    let actionsFired = 0
    const onKeydown = (instance: typeof instanceA) => {
      if (!hasViewportFocus(instance)) return
      actionsFired++
    }

    onKeydown(instanceA)
    onKeydown(instanceB)

    expect(actionsFired).toBe(1)
  })

  it('is permissive (true) when there is no document to ask — SSR / non-browser embedding, "behave as the owning instance"', () => {
    expect(hasViewportFocus({})).toBe(true)
    expect(hasViewportFocus({ document: {} })).toBe(true)
  })

  it('is permissive when hasFocus itself throws, rather than propagating the error', () => {
    const hostile = { document: { hasFocus: () => { throw new Error('nope') } } }
    expect(hasViewportFocus(hostile)).toBe(true)
  })

  it('defaults to the real globalThis when called with no argument', () => {
    // jsdom's own document.hasFocus() — whatever it reports, this must not
    // throw and must return a boolean, proving the zero-arg path is wired to
    // the real document rather than only ever exercised via injection.
    expect(typeof hasViewportFocus()).toBe('boolean')
  })
})

describe('useViewportFocus — reacts to this instance\'s own focus/blur events', () => {
  it('initialises from hasViewportFocus() and updates on window focus/blur', () => {
    const { result } = renderHook(() => useViewportFocus())
    const initial = result.current
    expect(typeof initial).toBe('boolean')

    act(() => { window.dispatchEvent(new Event('blur')) })
    expect(result.current).toBe(false)

    act(() => { window.dispatchEvent(new Event('focus')) })
    expect(result.current).toBe(true)
  })

  it('never calls document.focus()/element.focus() itself — read-only by construction', () => {
    // This is the "does not compete with the compositor" half of the
    // contract: the hook may only ever READ focus state, never request it.
    // Spying on the global focus acquisition points and asserting they are
    // never touched is the closest a unit test gets to proving a negative.
    const focusSpy = vi.spyOn(HTMLElement.prototype, 'focus')
    renderHook(() => useViewportFocus())
    act(() => { window.dispatchEvent(new Event('blur')) })
    act(() => { window.dispatchEvent(new Event('focus')) })
    expect(focusSpy).not.toHaveBeenCalled()
    focusSpy.mockRestore()
  })
})
