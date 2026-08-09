import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'

import {
  Z_DESKTOP_WIDGETS,
  WINDOW_Z_INACTIVE,
  WINDOW_Z_ACTIVE,
  WINDOW_Z_CLOSING_LIFT,
} from './zLayers'

// zLayers.test.ts — the desktop's stacking order.
//
// These relationships used to be implicit in numbers written in two different
// files, and they drifted: the widget column was `z-20`, the same value the
// ACTIVE window gets, so it rendered later in the DOM, won the tie, and floated
// over windows. Nothing failed — it just looked broken, and only a screenshot
// caught it.
//
// A test that merely re-read the constants would be a tautology, so this also
// checks that the components genuinely CONSUME them rather than reintroducing
// a literal.

describe('desktop stacking order', () => {
  it('ambient widgets sit below every window', () => {
    // Below the INACTIVE window too, not just the active one — a background
    // window must still cover the wallpaper furniture.
    expect(Z_DESKTOP_WIDGETS).toBeLessThan(WINDOW_Z_INACTIVE)
    expect(Z_DESKTOP_WIDGETS).toBeLessThan(WINDOW_Z_ACTIVE)
  })

  it('the focused window sits above an unfocused one', () => {
    expect(WINDOW_Z_ACTIVE).toBeGreaterThan(WINDOW_Z_INACTIVE)
  })

  it('a closing window lifts above the window revealed beneath it', () => {
    // The lift is added to the closing window's own base, so it only has to
    // clear the gap between the two window layers for the exit animation to
    // stay visible.
    expect(WINDOW_Z_INACTIVE + WINDOW_Z_CLOSING_LIFT).toBeGreaterThanOrEqual(WINDOW_Z_ACTIVE - WINDOW_Z_INACTIVE)
    expect(WINDOW_Z_CLOSING_LIFT).toBeGreaterThan(0)
  })

  it('the widget column takes its z-index from zLayers, not a literal', () => {
    const src = readFileSync('src/shell/DesktopWidgets.tsx', 'utf8')
    expect(src, 'DesktopWidgets must import the shared layer constant').toContain('Z_DESKTOP_WIDGETS')
    // A tailwind z-* utility on the column would silently override the
    // constant, which is exactly how the original bug was written.
    const container = src.match(/data-desktop-widgets[\s\S]{0,400}?>/)?.[0] ?? ''
    expect(container, 'the widget container must not carry a literal tailwind z-* class').not.toMatch(/\sz-\[?\d/)
  })

  it('Window takes its z-index from zLayers, not a literal', () => {
    const src = readFileSync('src/shell/Window.tsx', 'utf8')
    expect(src).toContain('WINDOW_Z_ACTIVE')
    expect(src).toContain('WINDOW_Z_INACTIVE')
    expect(src, 'the old inline ternary reintroduces the drift this file exists to prevent')
      .not.toMatch(/zBase\s*=\s*isActive\s*\?\s*\d+\s*:\s*\d+/)
  })
})
