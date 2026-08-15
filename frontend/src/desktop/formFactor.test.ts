import { describe, it, expect, vi, afterEach } from 'vitest'
import { activeFormFactor } from './store'
import { resolveViewportLayout, VIEWPORT_QUERIES } from '../shell/useViewport'

/**
 * The tablet band, pinned.
 *
 * `activeFormFactor()` used to be its own `innerWidth < 768` test, under a
 * comment claiming it mirrored shell/useViewport.ts exactly. It did not: that
 * file ALSO treats a coarse-pointer, hover-less viewport up to 1024px as
 * mobile. So between 768 and 1024 the touch shell was mounted while the layout
 * store answered `desktop`, and an iPad in portrait ran MobileStack while being
 * handed the desktop dock's twelve-item, small-tile geometry.
 *
 * Note what a naive test would have missed: at 390 the old rule was RIGHT, and
 * at 1440 it was right too. Only the band between the two breakpoints, and only
 * with a coarse pointer, exposed it — which is why the cases below are the two
 * tablet widths this project's own harnesses use, each asserted twice: once
 * with a finger and once with a mouse.
 */
function stubViewport(width: number, touch: boolean): void {
  vi.stubGlobal('innerWidth', width)
  vi.stubGlobal('matchMedia', (q: string) => ({
    matches: touch && q === VIEWPORT_QUERIES.touchTablet,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  }))
}

describe('activeFormFactor tracks the shell that is actually mounted', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  const cases = [
    ['a phone', 390, true, 'mobile'],
    ['a narrow window on a desktop', 500, false, 'mobile'],
    // The band the old width test got wrong.
    ['a touch tablet in portrait', 768, true, 'mobile'],
    ['a larger touch tablet', 834, true, 'mobile'],
    ['a touch tablet at the ceiling', 1024, true, 'mobile'],
    // Same widths, a mouse: these are desktops and must stay desktops, or the
    // fix would have traded one wrong answer for another.
    ['a mouse-driven window at tablet width', 834, false, 'desktop'],
    ['a desktop', 1440, false, 'desktop'],
  ] as const

  for (const [label, width, touch, want] of cases) {
    it(`${label} (${width}px, touch=${touch}) is ${want}`, () => {
      stubViewport(width, touch)
      expect(activeFormFactor()).toBe(want)
      // And it is the SAME answer the shell uses to decide what to mount.
      // Asserted directly so the two cannot merely agree by coincidence on the
      // widths this file happens to check.
      expect(activeFormFactor()).toBe(resolveViewportLayout())
    })
  }
})
