import { describe, it, expect } from 'vitest'
import {
  parseColor, toHex, contrast, accentText, accentSolid,
} from '../core/accentContrast'

// The accent is chosen by the USER, so no stylesheet value can promise that
// accent-coloured text is readable. These functions make the promise instead.

const DARK = '#08090c'   // --bg-base, dark theme
const LIGHT = '#f4f6fb'  // the shell surface the light theme actually renders

const c = (x: string) => parseColor(x)!

describe('colour parsing', () => {
  it('reads hex, short hex and rgb()', () => {
    expect(parseColor('#3b82f6')).toEqual({ r: 59, g: 130, b: 246 })
    expect(parseColor('3b82f6')).toEqual({ r: 59, g: 130, b: 246 })
    expect(parseColor('#abc')).toEqual({ r: 170, g: 187, b: 204 })
    expect(parseColor('rgb(59, 130, 246)')).toEqual({ r: 59, g: 130, b: 246 })
    expect(parseColor('not a colour')).toBeNull()
  })

  it('round-trips through hex', () => {
    expect(toHex(c('#3b82f6'))).toBe('#3b82f6')
  })
})

describe('contrast', () => {
  it('matches the WCAG extremes', () => {
    expect(contrast(c('#ffffff'), c('#000000'))).toBeCloseTo(21, 1)
    expect(contrast(c('#3b82f6'), c('#3b82f6'))).toBeCloseTo(1, 5)
  })

  it('is symmetric', () => {
    expect(contrast(c('#3b82f6'), c(LIGHT))).toBeCloseTo(contrast(c(LIGHT), c('#3b82f6')), 6)
  })
})

describe('accentText', () => {
  it('fixes the default blue on the light shell', () => {
    // The measured defect: #3b82f6 on #f4f6fb is 3.40:1.
    expect(contrast(c('#3b82f6'), c(LIGHT))).toBeLessThan(4.5)
    const fixed = accentText('#3b82f6', LIGHT)!
    expect(contrast(c(fixed), c(LIGHT))).toBeGreaterThanOrEqual(4.5)
  })

  it('holds for every accent a user might pick', () => {
    // Sampled around the hue circle at several saturations and lightnesses,
    // because the guarantee is about ANY colour, not about blue.
    const samples: string[] = []
    for (let h = 0; h < 360; h += 30) {
      for (const [s, l] of [[100, 50], [60, 70], [40, 30], [100, 85]] as const) {
        samples.push(hsl(h, s, l))
      }
    }
    for (const surface of [DARK, LIGHT]) {
      for (const a of samples) {
        const fixed = accentText(a, surface)!
        expect(
          contrast(c(fixed), c(surface)),
          `${a} on ${surface} -> ${fixed}`,
        ).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  it('leaves an accent that already passes alone', () => {
    const alreadyFine = '#0a3d62' // very dark blue on a light surface
    expect(contrast(c(alreadyFine), c(LIGHT))).toBeGreaterThan(4.5)
    expect(accentText(alreadyFine, LIGHT)).toBe(alreadyFine)
  })

  it('keeps the hue it was given', () => {
    // A user who picks red must not be handed blue. Compare the dominant
    // channel rather than recomputing hue: it is the property a user would
    // actually notice.
    const red = accentText('#ff0000', LIGHT)!
    const p = c(red)
    expect(p.r).toBeGreaterThan(p.g)
    expect(p.r).toBeGreaterThan(p.b)
  })
})

describe('accentSolid', () => {
  it('fixes white-on-accent for the default blue', () => {
    expect(contrast(c('#ffffff'), c('#3b82f6'))).toBeLessThan(4.5)
    const solid = accentSolid('#3b82f6')!
    expect(contrast(c('#ffffff'), c(solid))).toBeGreaterThanOrEqual(4.5)
  })

  it('adjusts in opposite directions for text and for fills', () => {
    // This is why there are two tokens rather than one. The two jobs pull
    // opposite ways, and no single value serves both.
    //
    // It takes TWO colours to show, which is the honest shape of it: a colour
    // that is too dark to read on a dark page is, for that same reason, already
    // fine under white. Two earlier versions of this test used one colour and
    // failed — both times the assumption was wrong and the code was right.

    // Too dark to READ on a dark page -> accentText must lighten it.
    const navy = '#1a3a8f'
    expect(contrast(c(navy), c(DARK))).toBeLessThan(4.5)
    const lightened = c(accentText(navy, DARK)!)
    expect(luminanceOf(lightened)).toBeGreaterThan(luminanceOf(c(navy)))
    expect(contrast(lightened, c(DARK))).toBeGreaterThanOrEqual(4.5)

    // Too light to carry WHITE -> accentSolid must darken it.
    const sky = '#7dd3fc'
    expect(contrast(c('#ffffff'), c(sky))).toBeLessThan(4.5)
    const darkened = c(accentSolid(sky)!)
    expect(luminanceOf(darkened)).toBeLessThan(luminanceOf(c(sky)))
    expect(contrast(c('#ffffff'), darkened)).toBeGreaterThanOrEqual(4.5)
  })

  it('leaves the default blue alone on a dark surface, because it already passes', () => {
    expect(contrast(c('#3b82f6'), c(DARK))).toBeGreaterThan(4.5)
    expect(accentText('#3b82f6', DARK)).toBe('#3b82f6')
  })
})

function luminanceOf(x: { r: number; g: number; b: number }): number {
  const f = (v: number) => { const n = v / 255; return n <= 0.03928 ? n / 12.92 : Math.pow((n + 0.055) / 1.055, 2.4) }
  return 0.2126 * f(x.r) + 0.7152 * f(x.g) + 0.0722 * f(x.b)
}

function hsl(h: number, s: number, l: number): string {
  const S = s / 100, L = l / 100
  const k = (n: number) => (n + h / 30) % 12
  const a = S * Math.min(L, 1 - L)
  const f = (n: number) => L - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)))
  const to = (v: number) => Math.round(v * 255).toString(16).padStart(2, '0')
  return `#${to(f(0))}${to(f(8))}${to(f(4))}`
}
