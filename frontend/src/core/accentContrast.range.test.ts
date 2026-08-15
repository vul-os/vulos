/**
 * The accent derivation has to hold for surfaces it was never handed.
 *
 * # The defect this closes
 *
 * ThemeProvider derives --accent-text against an ENUMERATED list of surfaces
 * (the three opaque backgrounds, plus an accent tint composited over each) and
 * used to target exactly 4.5:1 against the worst of them. That is correct for
 * every surface in the list and silently wrong for every surface outside it.
 *
 * Controls across this OS tint their own background: App Hub's install chip is
 * `color-mix(in srgb, var(--accent) 13%, transparent)`, `.accent-bg-soft` is
 * 14%, `.accent-bg-hover:hover` is 22%. Accent-coloured text on a 13% chip is a
 * DIFFERENT pair from accent-on-15%-soft, and it measured 4.21:1 dark /
 * 4.28:1 light while every token in the file said AA.
 *
 * So this file measures the derived value against the whole tint range rather
 * than against the one value the derivation happened to sample — including 13%,
 * which is not in ACCENT_TINT_RANGE and must pass anyway.
 *
 * # Why the compositing is done by hand here
 *
 * accentContrast.compositeOver() resolves colours through a 1x1 canvas, and
 * jsdom's canvas has no 2d context — it would return null, the test would
 * silently fall back to the opaque surface, and it would measure accent-on-page
 * instead of accent-on-tint. That is the exact shape of a gate that passes
 * while checking nothing. A tint over an opaque base is a plain alpha blend, so
 * it is computed directly.
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  AA_TARGET, AA_TEXT, ACCENT_TINT_RANGE,
  accentSolid, accentText, contrast, parseColor, toHex, worstSurfaceFor,
} from './accentContrast'

/** The palette every accent is judged against, straight from index.css. */
const SURFACES = {
  dark: ['#08090c', '#0f1116', '#171a21'],
  light: ['#ffffff', '#f7f8fa', '#eef0f4'],
} as const

/** The ten swatches Settings offers, plus the shipped default. */
const ACCENTS = [
  '#3b82f6', '#6366f1', '#8b5cf6', '#ec4899', '#f43f5e',
  '#f97316', '#f59e0b', '#22c55e', '#14b8a6', '#06b6d4',
  '#5b6cff',
]

/**
 * `color-mix(in srgb, accent P%, transparent)` painted over an opaque base.
 * Compositing a colour at alpha P over an opaque backdrop is a straight lerp.
 */
function tintOver(accent: string, pct: number, base: string): string {
  const a = parseColor(accent)!
  const b = parseColor(base)!
  const p = pct / 100
  return toHex({ r: a.r * p + b.r * (1 - p), g: a.g * p + b.g * (1 - p), b: a.b * p + b.b * (1 - p) })
}

/** Exactly what ThemeProvider builds, minus the canvas. */
function surfacesFor(accent: string, theme: 'dark' | 'light'): string[] {
  const opaque = [...SURFACES[theme]]
  const tinted = ACCENT_TINT_RANGE.flatMap((pct) => opaque.map((b) => tintOver(accent, pct, b)))
  return [...opaque, ...tinted]
}

function derivedText(accent: string, theme: 'dark' | 'light'): string {
  const worst = worstSurfaceFor(accent, surfacesFor(accent, theme))
  return accentText(accent, worst, AA_TARGET)!
}

describe('accent text stays legible on every surface it can land on', () => {
  for (const theme of ['dark', 'light'] as const) {
    it(`clears AA on the plain surfaces (${theme})`, () => {
      for (const accent of ACCENTS) {
        const fg = parseColor(derivedText(accent, theme))!
        for (const surface of SURFACES[theme]) {
          const ratio = contrast(fg, parseColor(surface)!)
          expect(ratio, `${accent} text on ${surface} (${theme}) = ${ratio.toFixed(2)}`).toBeGreaterThanOrEqual(AA_TEXT)
        }
      }
    })

    it(`clears AA on accent-tinted surfaces the derivation never sampled (${theme})`, () => {
      // 13% is App Hub's install chip — the measured defect. 5/8/10/12/14/18/20
      // bracket every other tint in index.css. None of these is in
      // ACCENT_TINT_RANGE; all of them must pass, which is the whole point.
      const unsampled = [5, 8, 10, 12, 13, 14, 18, 20]
      for (const accent of ACCENTS) {
        const fg = parseColor(derivedText(accent, theme))!
        for (const pct of unsampled) {
          for (const base of SURFACES[theme]) {
            const bg = tintOver(accent, pct, base)
            const ratio = contrast(fg, parseColor(bg)!)
            expect(
              ratio,
              `${accent} text on a ${pct}% tint over ${base} (${theme}) = ${ratio.toFixed(2)}`,
            ).toBeGreaterThanOrEqual(AA_TEXT)
          }
        }
      }
    })
  }
})

describe('a filled accent surface carries white text', () => {
  it('clears AA for every shipped accent — the .accent-bg / .btn-primary pair', () => {
    for (const accent of ACCENTS) {
      const solid = accentSolid(accent, '#ffffff', AA_TARGET)!
      const ratio = contrast(parseColor(solid)!, parseColor('#ffffff')!)
      expect(ratio, `white on ${accent} -> ${solid} = ${ratio.toFixed(2)}`).toBeGreaterThanOrEqual(AA_TEXT)
    }
  })

  it('moves the default blue off the measured 3.68:1', () => {
    // The raw accent under white, which is what `.accent-bg` used to paint and
    // what the mobile window-count badge measured at.
    const raw = contrast(parseColor('#3b82f6')!, parseColor('#ffffff')!)
    expect(raw).toBeLessThan(AA_TEXT)
    expect(raw).toBeCloseTo(3.68, 1)
    const fixed = contrast(parseColor(accentSolid('#3b82f6', '#ffffff', AA_TARGET)!)!, parseColor('#ffffff')!)
    expect(fixed).toBeGreaterThanOrEqual(AA_TEXT)
  })
})

/**
 * The numeric tests above prove `accentSolid()` produces a fill that white can
 * be read on. They say nothing about whether the stylesheet actually USES it —
 * and that gap is exactly where the defect lived: `.accent-bg` painted the raw
 * accent for months while `.btn-primary` two hundred lines away had already
 * been fixed and documented.
 *
 * So this reads the shipped CSS. It is a weak check on its own (it inspects a
 * token, not a pixel) and a strong one in combination: the pair "the derivation
 * is correct" + "the utility uses the derivation" is what makes the composited
 * e2e contrast gates able to stay green for the right reason.
 */
describe('the accent-fill utilities use the derived fill, not the raw accent', () => {
  // Resolved from the vitest root (frontend/), not from import.meta.url —
  // under Vite that is an http: URL and readFileSync rejects it.
  const css = readFileSync(resolve(process.cwd(), 'src/index.css'), 'utf8')

  // Utilities whose callers put --accent-contrast (white) on top of them.
  const FILL_UTILITIES = ['.accent-bg ', '.hover-accent-bg:hover ']

  for (const selector of FILL_UTILITIES) {
    it(`${selector.trim()} paints --accent-solid`, () => {
      const line = css.split('\n').find((l) => l.startsWith(selector))
      expect(line, `${selector} is missing from index.css`).toBeTruthy()
      expect(line).toContain('var(--accent-solid')
      // The raw accent may only appear as the fallback inside that var().
      expect(line!.replace(/var\(--accent-solid,\s*var\(--accent\)\)/g, ''))
        .not.toContain('var(--accent)')
    })
  }

  it('leaves the raw accent in place where nothing is read on top of it', () => {
    // Borders, rings and dots carry no text, so they keep the user's exact
    // colour. Deriving those too would drift the accent for no legibility gain.
    const border = css.split('\n').find((l) => l.startsWith('.accent-border '))
    expect(border).toContain('var(--accent)')
    expect(border).not.toContain('--accent-solid')
  })
})

describe('the headroom is real but not extravagant', () => {
  it('aims above AA so an unenumerated surface still clears it', () => {
    expect(AA_TARGET).toBeGreaterThan(AA_TEXT)
    // More than ~15% headroom would push accent text so far from the user's
    // colour that the accent stops being recognisable — a different failure.
    expect(AA_TARGET).toBeLessThan(AA_TEXT * 1.15)
  })
})
