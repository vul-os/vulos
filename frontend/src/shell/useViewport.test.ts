// useViewport — MOBILE-07 touch-tablet routing.
//
// This exists because of a defect that every green test in the suite missed: at
// 768×1024 with a finger, the shell mounted DesktopCanvas and served 12×12 px
// window controls. The layout predicate is the ONLY thing standing between a
// tablet and that canvas, so it is asserted directly, with each media query
// driven independently — a test that only ever flips both together would still
// pass if one half of the predicate were deleted.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useViewport, VIEWPORT_QUERIES } from './useViewport'

type Listener = () => void

// A matchMedia stub whose truth table the test controls per-query.
function installMatchMedia(matches: Record<string, boolean>) {
  const listeners = new Map<string, Set<Listener>>()
  const mm = vi.fn((query: string) => ({
    media: query,
    get matches() { return !!matches[query] },
    addEventListener: (_: string, fn: Listener) => {
      if (!listeners.has(query)) listeners.set(query, new Set())
      listeners.get(query)!.add(fn)
    },
    removeEventListener: (_: string, fn: Listener) => { listeners.get(query)?.delete(fn) },
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false, onchange: null,
  }))
  Object.defineProperty(window, 'matchMedia', { value: mm, writable: true, configurable: true })
  return {
    // Flip one query and fire only ITS listeners — the same thing a real browser
    // does, and the thing that catches a missing listener registration.
    set(query: string, value: boolean) {
      matches[query] = value
      act(() => { listeners.get(query)?.forEach(fn => fn()) })
    },
    listenerCount: (query: string) => listeners.get(query)?.size ?? 0,
  }
}

function setWidth(px: number) {
  Object.defineProperty(window, 'innerWidth', { value: px, writable: true, configurable: true })
}

const originalMatchMedia = window.matchMedia
const originalWidth = window.innerWidth

afterEach(() => {
  Object.defineProperty(window, 'matchMedia', { value: originalMatchMedia, writable: true, configurable: true })
  Object.defineProperty(window, 'innerWidth', { value: originalWidth, writable: true, configurable: true })
})

describe('useViewport — width still leads', () => {
  beforeEach(() => { installMatchMedia({}) })

  it('a 390px phone is mobile', () => {
    setWidth(390)
    expect(renderHook(() => useViewport()).result.current).toBe('mobile')
  })

  it('a 1440px desktop is desktop', () => {
    setWidth(1440)
    expect(renderHook(() => useViewport()).result.current).toBe('desktop')
  })

  it('767 is mobile and 768 alone is not — the breakpoint is exactly where it claims', () => {
    setWidth(767)
    expect(renderHook(() => useViewport()).result.current).toBe('mobile')
    setWidth(768)
    expect(renderHook(() => useViewport()).result.current).toBe('desktop')
  })
})

describe('useViewport — MOBILE-07: a touch tablet is not a small desktop', () => {
  it('768×1024 with a coarse, hoverless pointer is MOBILE (the 12px-window-control defect)', () => {
    setWidth(768)
    installMatchMedia({ [VIEWPORT_QUERIES.touchTablet]: true })
    expect(renderHook(() => useViewport()).result.current).toBe('mobile')
  })

  it('834×1194 with a coarse, hoverless pointer is MOBILE', () => {
    setWidth(834)
    installMatchMedia({ [VIEWPORT_QUERIES.touchTablet]: true })
    expect(renderHook(() => useViewport()).result.current).toBe('mobile')
  })

  it('a 1280px touchscreen LAPTOP stays desktop — it has a hovering pointer, so the query does not match', () => {
    setWidth(1280)
    // hover: hover ⇒ TOUCH_QUERY is false even though the screen accepts touch.
    installMatchMedia({ [VIEWPORT_QUERIES.touchTablet]: false })
    expect(renderHook(() => useViewport()).result.current).toBe('desktop')
  })

  it('the touch query carries BOTH pointer:coarse AND hover:none, and a max-width', () => {
    // Guards the predicate itself: dropping `hover: none` would sweep touchscreen
    // laptops into the phone stack, and dropping the width cap would sweep every
    // large touch display in.
    expect(VIEWPORT_QUERIES.touchTablet).toContain('(pointer: coarse)')
    expect(VIEWPORT_QUERIES.touchTablet).toContain('(hover: none)')
    expect(VIEWPORT_QUERIES.touchTablet).toContain('max-width: 1024px')
  })
})

describe('useViewport — reacts to BOTH queries', () => {
  it('rotating a tablet out of the touch range re-renders as desktop', () => {
    setWidth(834)
    const mm = installMatchMedia({ [VIEWPORT_QUERIES.touchTablet]: true })
    const { result } = renderHook(() => useViewport())
    expect(result.current).toBe('mobile')

    // Landscape: still wider than MOBILE_BREAKPOINT (so the narrow query never
    // changes), but now past TOUCH_STACK_MAX. Only a listener on the touch query
    // can notice this.
    setWidth(1194)
    mm.set(VIEWPORT_QUERIES.touchTablet, false)
    expect(result.current).toBe('desktop')
  })

  it('crossing the narrow breakpoint re-renders as mobile', () => {
    setWidth(1440)
    const mm = installMatchMedia({ [VIEWPORT_QUERIES.narrow]: false })
    const { result } = renderHook(() => useViewport())
    expect(result.current).toBe('desktop')

    setWidth(400)
    mm.set(VIEWPORT_QUERIES.narrow, true)
    expect(result.current).toBe('mobile')
  })

  it('subscribes to both queries, not just one', () => {
    setWidth(900)
    const mm = installMatchMedia({})
    renderHook(() => useViewport())
    expect(mm.listenerCount(VIEWPORT_QUERIES.narrow)).toBe(1)
    expect(mm.listenerCount(VIEWPORT_QUERIES.touchTablet)).toBe(1)
  })

  it('unsubscribes from both on unmount', () => {
    setWidth(900)
    const mm = installMatchMedia({})
    const { unmount } = renderHook(() => useViewport())
    unmount()
    expect(mm.listenerCount(VIEWPORT_QUERIES.narrow)).toBe(0)
    expect(mm.listenerCount(VIEWPORT_QUERIES.touchTablet)).toBe(0)
  })
})
