import { describe, it, expect, afterEach } from 'vitest'
import { readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { createElement } from 'react'
import { render, cleanup } from '@testing-library/react'

import { APP_LOGOS, INSET_LOGOS, ART_RADIUS, AppIconTile } from './AppIcons'

// AppIcons.test.ts — keeps INSET_LOGOS honest.
//
// Most icons we bundle draw their OWN full-bleed rounded square as a
// background rect, so painting the shell's neutral tile behind them produces a
// plate inside a plate: the mark renders inset, with a visible border, dulled
// and smaller than the art plates beside it in the same launcher row. A few are
// bare marks on transparency (the Chrome disc, the Firefox fox) and DO need the
// tile to sit on.
//
// Which is which is a property of the SVG FILES, not a matter of taste, and a
// hand-maintained list of 71 drifts the moment somebody adds an icon. So this
// re-derives the answer by parsing every referenced file and fails if the
// checked-in set disagrees. Adding an icon with the wrong shape fails here
// rather than looking subtly wrong in the launcher.

const ICON_ROOT = 'public'

type Rect = { w: number; h: number; x: number; y: number; rx: number }

// Attributes must be matched on a boundary. `x="..."` without one also matches
// the x inside `rx="113"`, which silently reports every plate as offset and so
// classifies the entire set as bare — that exact slip produced a 0-plate/71-bare
// reading while writing this.
function attr(tag: string, name: string): number | null {
  const m = tag.match(new RegExp(`(?:^|\\s)${name}="([\\d.]+)"`))
  return m ? parseFloat(m[1]) : null
}

/** The radius of the icon's own full-bleed plate, or null if it has none. */
function ownPlateRadius(svg: string): number | null {
  const vb = svg.match(/viewBox="0 0 ([\d.]+) ([\d.]+)"/)
  if (!vb) return null
  const [w, h] = [parseFloat(vb[1]), parseFloat(vb[2])]

  for (const m of svg.matchAll(/<rect\b[^>]*>/g)) {
    const r: Rect = {
      w: attr(m[0], 'width') ?? -1,
      h: attr(m[0], 'height') ?? -1,
      x: attr(m[0], 'x') ?? 0,
      y: attr(m[0], 'y') ?? 0,
      rx: attr(m[0], 'rx') ?? 0,
    }
    if (r.w === w && r.h === h && r.x === 0 && r.y === 0 && r.rx > 0) {
      return r.rx / w
    }
  }
  return null
}

function readIcon(url: string): string | null {
  const file = join(ICON_ROOT, url.replace(/^\//, ''))
  return existsSync(file) ? readFileSync(file, 'utf8') : null
}

describe('APP_LOGOS / INSET_LOGOS', () => {
  it('every referenced logo file exists', () => {
    const missing = Object.entries(APP_LOGOS)
      .filter(([, url]) => readIcon(url) === null)
      .map(([id]) => id)
    expect(missing, 'APP_LOGOS points at files that are not in public/icons').toEqual([])
  })

  it('INSET_LOGOS is exactly the set of logos with no plate of their own', () => {
    const bare: string[] = []
    const plated: string[] = []

    for (const [id, url] of Object.entries(APP_LOGOS)) {
      const svg = readIcon(url)
      if (svg === null) continue
      ;(ownPlateRadius(svg) === null ? bare : plated).push(id)
    }

    // Guard the guard: if the parser ever stops recognising plates, both
    // assertions below could pass vacuously while checking nothing.
    expect(plated.length, 'no logo parsed as having its own plate — the parser is broken').toBeGreaterThan(50)
    expect(bare.length, 'every logo parsed as bare — the parser is broken').toBeLessThan(20)

    expect([...INSET_LOGOS].sort(), 'a logo that draws its own plate must NOT be inset (it would render as a plate inside a plate), and a bare mark MUST be').toEqual(bare.sort())
  })

  it('ART_RADIUS matches the corner radius the bundled artwork actually uses', () => {
    // Terminal is the reference tile the whole set is measured against: the
    // founder called it the one good icon, so the art plates must sit at its
    // radius rather than a rounder invented one.
    const terminal = readIcon(APP_LOGOS.terminal)
    expect(terminal, 'terminal.svg is the reference tile and must exist').not.toBeNull()

    const r = ownPlateRadius(terminal as string)
    expect(r, 'terminal.svg no longer draws its own full-bleed plate').not.toBeNull()
    expect(Math.abs((r as number) - ART_RADIUS)).toBeLessThan(0.01)
  })
})

// AppIconTile must actually CONSULT INSET_LOGOS when it renders, not just
// carry a correct-looking classification beside dead render code. (That was
// the real bug: INSET_LOGOS + this file's derivation existed and passed, but
// the brandLogo branch in AppIconTile rendered every bundled logo at a fixed
// 70% inset on the neutral tile regardless of the set — terminal.svg, which
// is full-bleed, still rendered as a plate inside a plate.) These tests
// render the real component and inspect the DOM the two classes actually
// produce, so a future edit that reintroduces a fixed 70%-inset render (or
// drops the edge-to-edge one) fails here even though the classification
// tests above would stay green.
describe('AppIconTile renders full-bleed logos edge-to-edge, inset logos on the neutral tile', () => {
  afterEach(cleanup)

  it('a full-bleed logo (terminal, not in INSET_LOGOS) fills the tile and drops the neutral chrome', () => {
    expect(INSET_LOGOS.has('terminal'), 'terminal.svg is full-bleed; this test assumes it is not in INSET_LOGOS').toBe(false)
    const { container } = render(createElement(AppIconTile, { id: 'terminal', size: 48 }))
    const tile = container.querySelector('.vula-itile')
    const img = container.querySelector('img')
    expect(tile?.getAttribute('data-logo'), 'a full-bleed logo tile must carry data-logo so the neutral chrome CSS steps out of the way').toBe('')
    expect(img?.getAttribute('src')).toBe(APP_LOGOS.terminal)
    expect(img?.style.width, 'a full-bleed logo must fill the tile, not sit inset at 70%').toBe('100%')
    expect(img?.style.height).toBe('100%')
  })

  it('an inset logo (firefox, in INSET_LOGOS) keeps the neutral tile and the 70% inset mark', () => {
    expect(INSET_LOGOS.has('firefox'), 'firefox.svg is a bare mark on transparency; this test assumes it is in INSET_LOGOS').toBe(true)
    const { container } = render(createElement(AppIconTile, { id: 'firefox', size: 48 }))
    const tile = container.querySelector('.vula-itile')
    const img = container.querySelector('img')
    expect(tile?.hasAttribute('data-logo'), 'a bare-mark logo must NOT get the edge-to-edge treatment — it has nothing to sit on').toBe(false)
    expect(img?.getAttribute('src')).toBe(APP_LOGOS.firefox)
    expect(img?.style.width).toBe('70%')
    expect(img?.style.height).toBe('70%')
  })
})
