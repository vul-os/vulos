/**
 * The six --status-*-text tokens, re-derived from the source tree.
 *
 * # What this exists to stop
 *
 * `text-success` / `text-warning` / `text-danger` used to paint --status-*
 * itself. Those values are FILLS — the dot on a widget, the bar in Disk Usage,
 * the ground under a badge, the 34% border, the `-soft` tints color-mix'd out
 * of them. As 14px text they measured 3.30 / 3.19 / 4.83 on the light canvas
 * and 2.15 / 2.10 / 2.80 on their own 24% tint; dark --status-danger measured
 * 4.23 on --bg-active. One colour cannot be both a fill and a text colour.
 *
 * # Why this test reads the repo instead of holding a table
 *
 * A test that pins six hexes it was handed proves only that nobody edited the
 * hexes. This one re-derives them:
 *
 *   - the SURFACES come from index.css's own --bg-* tokens, per theme. Move
 *     --bg-hover and the expected values move with it.
 *   - the TINT PERCENTAGES come from every `color-mix(--status-X N%)` in src,
 *     not from a constant. Add a 30% hover tint anywhere and this test widens
 *     its own enumeration and fails until the token can carry it. That is the
 *     hole the accent work fell into from the other side: --accent-text was
 *     derived against ONE sampled tint, so a control that tinted its own
 *     background more deeply measured a pair the derivation never aimed at,
 *     and came out at 4.21:1 with every token reporting AA.
 *   - the EXPECTED VALUE is deriveTextVariant()'s output, so the assertion is
 *     both a floor (>= 4.5 everywhere) and a ceiling (no darker than it needs
 *     to be — every point of headroom drags danger further from red).
 *
 * The e2e gate (e2e/status-surfaces.e2e.ts) then measures the same six on REAL
 * composited pixels, because no source-reading check can see an `opacity` in
 * the ancestor chain.
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, it, expect } from 'vitest'

import {
  AA_TEXT, AA_TARGET, STATUS_TINT_RANGE,
  parseColor, toHex, contrast, tintOver, deriveTextVariant, type Rgb,
} from './accentContrast'

const SRC = join(dirname(fileURLToPath(import.meta.url)), '..')
const INDEX_CSS = join(SRC, 'index.css')
const css = readFileSync(INDEX_CSS, 'utf8')

const TONES = ['success', 'warning', 'danger'] as const
type Tone = (typeof TONES)[number]

/**
 * The three theme states index.css supports. The third is the one that is
 * always forgotten: a System-Light user on a paint that happened before
 * main.jsx stamped data-theme. Miss it and the first frame of first boot is
 * the frame that fails, which is exactly how setup.css's wizard tokens were
 * found to be incomplete.
 */
const THEME_BLOCKS: { name: string; selector: string; light: boolean }[] = [
  { name: 'dark (:root default)', selector: ':root {', light: false },
  { name: 'light ([data-theme="light"])', selector: '[data-theme="light"] {', light: true },
  { name: 'light (prefers-color-scheme fallback)', selector: ':root:not([data-theme]) {', light: true },
]

/** The text of one declaration block, by brace matching from its selector. */
function block(selector: string): string {
  const start = css.indexOf(selector)
  if (start < 0) throw new Error(`index.css has no \`${selector}\` block`)
  let depth = 0
  for (let i = start + selector.length - 1; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}') {
      depth--
      if (depth === 0) return css.slice(start, i)
    }
  }
  throw new Error(`unterminated \`${selector}\` block`)
}

function tokenIn(text: string, name: string): string | null {
  const m = new RegExp(`--${name}\\s*:\\s*([^;]+);`).exec(text)
  return m ? m[1].trim() : null
}

/**
 * Every self-tint percentage the source tree draws status text ON TOP OF.
 *
 * Scanned, not declared: `bg-danger-soft` is 12% and `--status-warning-soft` is
 * 14%, but Packages and Drivers deepen the SAME text to 16% and 24% on hover
 * with an inline color-mix, and Home's chips use 18% and 20%. A constant would
 * have covered the two tokens and missed the four that actually bind.
 *
 * A tint only counts when STATUS TEXT IS DRAWN ON IT. Two filters do that, and
 * both were written because the naive version got it wrong:
 *
 *   - `background` / `bg-[`, with no `border` in between. The 30–42% band in
 *     this tree is entirely borders (`border-danger-soft` is 34%, Home's chip
 *     outlines are 38% and 40%). A border is a 1px line BESIDE the text, not
 *     the surface UNDER it.
 *   - a status text colour within ~160 characters — the same className string
 *     or the same style object. Messages.tsx paints a 55% danger wash over a
 *     failed attachment thumbnail and writes "Failed" on it in #fff; that
 *     surface never carries a status text colour, and folding 55% in would
 *     have driven light success past #052 chasing a pair nobody draws.
 */
function scannedTintPercentages(): { pcts: number[]; files: number } {
  const pcts = new Set<number>(STATUS_TINT_RANGE)
  let files = 0
  const BG_TINT = /(?:background|bg-\[)(?:(?!border)[^;\n]){0,90}?var\(--status-(?:success|warning|danger)\)[_\s]+(\d+(?:\.\d+)?)%/g
  const SOFT_TOKEN = /--status-(?:success|warning|danger)-soft\s*:\s*color-mix\([^;]*?\)[_\s]+(\d+(?:\.\d+)?)%/g
  const NEAR_STATUS_TEXT = /(?:^|[\s'"`{:])text-(?:success|warning|danger)\b|--status-(?:success|warning|danger)-text/
  const walk = (dir: string) => {
    for (const e of readdirSync(dir)) {
      if (e === 'node_modules' || e === 'dist') continue
      const p = join(dir, e)
      if (statSync(p).isDirectory()) { walk(p); continue }
      if (!/\.(css|tsx?|jsx?)$/.test(e)) continue
      files++
      const text = readFileSync(p, 'utf8')
      BG_TINT.lastIndex = 0
      let m: RegExpExecArray | null
      while ((m = BG_TINT.exec(text))) {
        const window = text.slice(Math.max(0, m.index - 160), m.index + m[0].length + 160)
        if (NEAR_STATUS_TEXT.test(window)) pcts.add(Number(m[1]))
      }
      // The `-soft` tokens are unconditional: `bg-*-soft text-*` is the single
      // most common status pairing in the OS.
      SOFT_TOKEN.lastIndex = 0
      while ((m = SOFT_TOKEN.exec(text))) pcts.add(Number(m[1]))
    }
  }
  walk(SRC)
  return { pcts: [...pcts].sort((a, b) => a - b), files }
}

const { pcts: TINTS, files: SCANNED_FILES } = scannedTintPercentages()

/**
 * Surfaces a status string is painted on, for one theme and one candidate.
 *
 * PLAIN: all five --bg-* steps, --bg-active included — a status label inside a
 * pressed row is a real pair, and it is where dark --status-danger measured
 * 4.23:1.
 *
 * TINTED: the scanned percentages, over the four PANEL surfaces. --bg-active is
 * deliberately not a tint base: it is a pressed state held only while the
 * pointer is down, and nothing in the OS composites a status chip on top of one.
 * Stacking them would push light success to near-black and dark danger to pale
 * pink for a pair that is never drawn. Naming the exclusion is the point — an
 * unstated one is how an enumeration silently stops covering the thing it named.
 */
const TINT_BASES = ['base', 'surface', 'elevated', 'hover'] as const
const ALL_BASES = ['base', 'surface', 'elevated', 'hover', 'active'] as const

function surfacesFor(bg: Record<string, Rgb>, candidate: Rgb): { name: string; rgb: Rgb }[] {
  const out: { name: string; rgb: Rgb }[] = []
  for (const b of ALL_BASES) out.push({ name: `--bg-${b}`, rgb: bg[b] })
  for (const b of TINT_BASES) {
    for (const t of TINTS) out.push({ name: `${t}% tint over --bg-${b}`, rgb: tintOver(candidate, t, bg[b]) })
  }
  return out
}

describe('--status-*-text is derived, not picked', () => {
  it('scans a source tree that is actually there', () => {
    // A scan that walked nothing would report an empty tint set and every
    // token would pass against a two-surface enumeration.
    expect(SCANNED_FILES).toBeGreaterThan(200)
    expect(TINTS).toEqual(expect.arrayContaining([12, 14, 16, 18, 20, 24]))
  })

  for (const theme of THEME_BLOCKS) {
    describe(theme.name, () => {
      const text = block(theme.selector)
      const bg: Record<string, Rgb> = {}
      for (const b of ALL_BASES) {
        const v = tokenIn(text, `bg-${b}`)
        const p = v && parseColor(v)
        if (p) bg[b] = p
      }

      it('declares all five --bg-* surfaces', () => {
        expect(Object.keys(bg).sort()).toEqual([...ALL_BASES].sort())
      })

      for (const tone of TONES) {
        it(`--status-${tone}-text is the ${tone} fill moved just far enough`, () => {
          const fillRaw = tokenIn(text, `status-${tone}`)
          const textRaw = tokenIn(text, `status-${tone}-text`)
          expect(fillRaw, `--status-${tone} missing from ${theme.name}`).toBeTruthy()
          expect(
            textRaw,
            `--status-${tone}-text missing from ${theme.name}. All three theme states need it: ` +
            'the one that is skipped is the one a System-Light user sees on first paint.',
          ).toBeTruthy()

          const fill = parseColor(fillRaw!)!
          const shipped = parseColor(textRaw!)!
          const expected = deriveTextVariant(fill, (c) => surfacesFor(bg, c).map((s) => s.rgb), AA_TARGET)
          expect(
            toHex(shipped),
            `--status-${tone}-text in ${theme.name}: shipped ${toHex(shipped)}, ` +
            `derived ${toHex(expected)} from --status-${tone} ${toHex(fill)}. ` +
            'This is a ceiling as well as a floor: a darker value than the derivation ' +
            'asks for is contrast bought by making danger stop looking like danger.',
          ).toBe(toHex(expected))
        })

        it(`--status-${tone}-text clears AA on every surface it is drawn on`, () => {
          const textRaw = tokenIn(text, `status-${tone}-text`)
          const shipped = parseColor(textRaw!)!
          const failures = surfacesFor(bg, shipped)
            .map((s) => ({ ...s, ratio: contrast(shipped, s.rgb) }))
            .filter((s) => s.ratio < AA_TEXT)
            .map((s) => `${s.ratio.toFixed(2)} on ${s.name} (${toHex(s.rgb)})`)
          expect(failures, `--status-${tone}-text ${toHex(shipped)} in ${theme.name}`).toEqual([])
        })
      }
    })
  }
})

/**
 * The fill token must not appear in a `color:` position anywhere in src.
 *
 * This is the half that the token split alone does not buy. Re-pointing the
 * three @utility rules fixes `text-danger` and does NOTHING for the
 * arbitrary-value spelling of the same thing, which is how 109 of the 138
 * sites were written — it bypasses the utility entirely. There is now one way
 * to say it, and this keeps it that way.
 *
 * (This file names the offending spellings only as regexes, never as literal
 * strings, so that it is subject to its own rule rather than exempt from it.)
 *
 * No exception list. The four graphic uses that DID sit in a `color:` position
 * (RecentsTab's direction strokes, ContactsTab's source dots) were renamed to
 * `stroke:` and `dot:`, because a colour that is not text should not be spelled
 * like one. An allowlist here would have been a place to put the next one.
 */
describe('no fill token in a text position', () => {
  const OFFENDERS = [
    { re: /text-\[(?:color:)?var\(--status-(?:success|warning|danger)\)\]/g, what: 'arbitrary-value text utility — use the semantic text-<tone> class' },
    { re: /(?<!-)color:\s*['"]?var\(--status-(?:success|warning|danger)\)/g, what: 'fill token in a color: position — use the -text variant' },
  ]

  const files: string[] = []
  const walk = (dir: string) => {
    for (const e of readdirSync(dir)) {
      if (e === 'node_modules' || e === 'dist') continue
      const p = join(dir, e)
      if (statSync(p).isDirectory()) { walk(p); continue }
      if (/\.(css|tsx?|jsx?)$/.test(e)) files.push(p)
    }
  }
  walk(SRC)

  it('walked the source tree', () => {
    expect(files.length).toBeGreaterThan(200)
    // index.css is where the utilities live, so a scan that skipped it would
    // miss the rule's most important file. No file is exempt, this one
    // included.
    expect(files).toContain(INDEX_CSS)
  })

  it('finds none', () => {
    const hits: string[] = []
    for (const f of files) {
      const text = readFileSync(f, 'utf8')
      for (const { re, what } of OFFENDERS) {
        re.lastIndex = 0
        let m: RegExpExecArray | null
        while ((m = re.exec(text))) {
          hits.push(`${relative(SRC, f)}: ${m[0]}  — ${what}`)
        }
      }
    }
    expect([...new Set(hits)].sort()).toEqual([])
  })
})
