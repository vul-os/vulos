/**
 * The six --status-*-text tokens, measured on REAL COMPOSITED PIXELS.
 *
 * # Why this exists alongside the unit test
 *
 * src/core/accentContrast.status.test.ts re-derives the six values from
 * index.css and proves the arithmetic. It reads tokens. Tokens are exactly what
 * a contrast check cannot trust: the desktop watermark was `color: var(--accent)`
 * — correct in every stylesheet — and rendered at 2.80:1 because an
 * `opacity: 0.85` sat beside it, and Contacts' empty state "passed" for months
 * because the scanner's regex could not read oklch(). The value in the file and
 * the pixel on the screen are two different things, and only one of them is what
 * a person reads.
 *
 * So this suite builds the surfaces out of the SHIPPED stylesheet — the real
 * --bg-* tokens, the real `bg-*-soft` utilities, the real color-mix() — inside
 * the running shell, and reads the pixels back through a canvas.
 *
 * # Why it constructs a probe instead of walking the app
 *
 * Because a walk only measures what the mock backend happens to render. Walking
 * all 34 Settings sections in both themes surfaced exactly EIGHT status-coloured
 * strings and five distinct pairs — it never rendered a single soft-tinted chip,
 * which is the pair that binds. A sweep that finds nothing reports clean.
 * settings-contrast.e2e.ts keeps the walk (it catches defects this cannot see,
 * anywhere in the OS); this covers the pairs the walk cannot reach.
 */
import { test, expect } from '@playwright/test'
import { bootShell } from './contrast-scan.js'

const TONES = ['success', 'warning', 'danger'] as const
/**
 * Percentages the OS tints a status surface to under status text. Kept in step
 * with the source tree by accentContrast.status.test.ts, which SCANS for them
 * and fails if a deeper one appears; this list is the rendered mirror of that
 * scan, so a change there shows up as a failure here too.
 */
const TINTS = [12, 14, 16, 18, 20, 24]
const BASES = ['base', 'surface', 'elevated', 'hover', 'active']
/** --bg-active is a pressed row; nothing composites a tinted chip onto one. */
const TINT_BASES = ['base', 'surface', 'elevated', 'hover']

interface Cell { label: string; ratio: number; need: number; fg: string; bg: string }

/**
 * Builds the probe, measures it, tears it down. Returns one row per pair, plus
 * two synthetic controls whose verdict is known.
 */
const PROBE = (args: { tones: string[]; tints: number[]; bases: string[]; tintBases: string[] }) => {
  const { tones, tints, bases, tintBases } = args
  const host = document.createElement('div')
  host.id = '__status_probe__'
  // Off-screen but LAID OUT and painted: display:none or visibility:hidden
  // would give getComputedStyle a colour and no composite, which is how a
  // measurement harness reports a ratio for something the compositor never drew.
  host.style.cssText = 'position:fixed;left:-10000px;top:0;width:900px;z-index:-1'
  document.body.appendChild(host)

  const mk = (parent: HTMLElement, background: string) => {
    const d = document.createElement('div')
    d.style.background = background
    d.style.padding = '4px'
    parent.appendChild(d)
    return d
  }
  const say = (parent: HTMLElement, colour: string, label: string) => {
    const s = document.createElement('span')
    s.style.color = colour
    s.style.fontSize = '14px'
    s.style.fontWeight = '400'
    s.dataset.probe = label
    s.textContent = 'Ready · Unreachable · shown once, copy it now'
    parent.appendChild(s)
    return s
  }

  const targets: { el: HTMLElement; label: string }[] = []
  for (const tone of tones) {
    for (const b of bases) {
      const plain = mk(host, `var(--bg-${b})`)
      const label = `--status-${tone}-text on --bg-${b}`
      targets.push({ el: say(plain, `var(--status-${tone}-text)`, label), label })
    }
    for (const b of tintBases) {
      const plain = mk(host, `var(--bg-${b})`)
      for (const t of tints) {
        const tinted = mk(plain, `color-mix(in srgb, var(--status-${tone}) ${t}%, transparent)`)
        const label = `--status-${tone}-text on ${t}% --status-${tone} over --bg-${b}`
        targets.push({ el: say(tinted, `var(--status-${tone}-text)`, label), label })
      }
    }
    // The shipped `-soft` utility itself, not a hand-written percentage: if
    // --status-danger-soft is ever re-mixed, this catches it without anyone
    // remembering to update the list above.
    for (const b of tintBases) {
      const plain = mk(host, `var(--bg-${b})`)
      const tinted = mk(plain, `var(--status-${tone}-soft)`)
      const label = `--status-${tone}-text on --status-${tone}-soft over --bg-${b}`
      targets.push({ el: say(tinted, `var(--status-${tone}-text)`, label), label })
    }
  }

  // ── Controls. A harness that cannot fail is not a measurement. ───────────
  const ctlBase = mk(host, '#ffffff')
  targets.push({ el: say(ctlBase, '#c8c8c8', 'CONTROL-BAD #c8c8c8 on #ffffff'), label: 'CONTROL-BAD #c8c8c8 on #ffffff' })
  targets.push({ el: say(ctlBase, '#000000', 'CONTROL-GOOD #000000 on #ffffff'), label: 'CONTROL-GOOD #000000 on #ffffff' })

  // ── Measure, the same way e2e/contrast-scan.ts does. ─────────────────────
  const cv = document.createElement('canvas')
  cv.width = 1; cv.height = 1
  const ctx = cv.getContext('2d', { willReadFrequently: true })!
  type C = { r: number; g: number; b: number; a: number }
  const parse = (c: string): C | null => {
    if (!c || c === 'none' || c === 'transparent') return null
    ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = '#000'; ctx.fillStyle = c
    ctx.fillRect(0, 0, 1, 1)
    const d = ctx.getImageData(0, 0, 1, 1).data
    return { r: d[0], g: d[1], b: d[2], a: d[3] / 255 }
  }
  const over = (f: C, g: C): C => ({
    r: f.r * f.a + g.r * (1 - f.a), g: f.g * f.a + g.g * (1 - f.a),
    b: f.b * f.a + g.b * (1 - f.a), a: 1,
  })
  const lum = (c: C) => {
    const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4) }
    return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b)
  }
  const ratio = (a: C, b: C) => {
    const L1 = lum(a), L2 = lum(b)
    return (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05)
  }
  const chainOpacity = (el: Element) => {
    let o = 1
    let a: Element | null = el
    while (a && a !== document.documentElement) { o *= parseFloat(getComputedStyle(a).opacity || '1'); a = a.parentElement }
    return o
  }
  const bgOf = (el: Element): C => {
    const st: { c: C; o: number }[] = []
    let a: Element | null = el
    while (a) {
      const c = parse(getComputedStyle(a).backgroundColor)
      if (c && c.a > 0) st.push({ c, o: parseFloat(getComputedStyle(a).opacity || '1') })
      if (a === document.documentElement) break
      a = a.parentElement
    }
    let base: C = { r: 255, g: 255, b: 255, a: 1 }
    for (let i = st.length - 1; i >= 0; i--) base = over({ ...st[i].c, a: st[i].c.a * st[i].o }, base)
    return base
  }

  const out: Cell[] = []
  for (const { el, label } of targets) {
    const cs = getComputedStyle(el)
    const fg = parse(cs.color)
    const bg = bgOf(el)
    if (!fg) continue
    const eff = over({ ...fg, a: fg.a * chainOpacity(el) }, bg)
    out.push({
      label,
      ratio: +ratio(eff, bg).toFixed(2),
      need: 4.5,
      fg: `rgb(${Math.round(eff.r)},${Math.round(eff.g)},${Math.round(eff.b)})`,
      bg: `rgb(${Math.round(bg.r)},${Math.round(bg.g)},${Math.round(bg.b)})`,
    })
  }
  host.remove()
  return out
}

for (const theme of ['dark', 'light'] as const) {
  test(`status text clears AA on every surface it is drawn on (${theme})`, async ({ page }) => {
    test.setTimeout(120_000)
    await bootShell(page, theme)
    const cells: Cell[] = await page.evaluate(PROBE, {
      tones: [...TONES], tints: TINTS, bases: BASES, tintBases: TINT_BASES,
    })

    // The probe must have rendered. 3 tones x (5 plain + 4x6 tinted + 4 soft)
    // plus 2 controls.
    expect(cells.length, 'the probe rendered nothing — every assertion below would be vacuous')
      .toBe(TONES.length * (BASES.length + TINT_BASES.length * TINTS.length + TINT_BASES.length) + 2)

    // Live sensitivity check, run on every invocation rather than trusted from
    // a mutation someone did once: a pair that IS sub-AA must be reported as
    // sub-AA, and a pair that is fine must not be. Both are synthetic constants,
    // so neither can go stale the way a control pinned to a real token does.
    const bad = cells.find((c) => c.label.startsWith('CONTROL-BAD'))!
    const good = cells.find((c) => c.label.startsWith('CONTROL-GOOD'))!
    expect(bad.ratio, 'the harness cannot see a failing pair').toBeLessThan(4.5)
    expect(good.ratio, 'the harness reports black-on-white as failing').toBeGreaterThan(20)

    const failures = cells
      .filter((c) => !c.label.startsWith('CONTROL'))
      .filter((c) => c.ratio + 0.0001 < c.need)
      .map((c) => `${c.ratio} (need ${c.need})  ${c.label}  ${c.fg} on ${c.bg}`)
    expect(failures.sort()).toEqual([])
  })
}

/**
 * The FILL tokens are still legible as MARKS.
 *
 * --status-* is what the widget dot, the Disk Usage bar and the badge ground
 * are painted in, and WCAG holds those to 3:1 against their surround rather
 * than 4.5:1. The value half of "the fill stays a fill" is asserted on the
 * tokens in core/accentContrast.status.test.ts (a saturation and lightness
 * band, so a fill deepened to rescue text fails there); this is the half that
 * has to be read off the rendered page, because --bg-base is itself resolved
 * through the theme.
 */
for (const theme of ['dark', 'light'] as const) {
  test(`status fills stay legible as marks (${theme})`, async ({ page }) => {
    await bootShell(page, theme)
    const got = await page.evaluate((tones: string[]) => {
      const rs = getComputedStyle(document.documentElement)
      const cv = document.createElement('canvas')
      cv.width = 1; cv.height = 1
      const ctx = cv.getContext('2d', { willReadFrequently: true })!
      const px = (c: string) => {
        ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = '#000'; ctx.fillStyle = c
        ctx.fillRect(0, 0, 1, 1)
        const d = ctx.getImageData(0, 0, 1, 1).data
        return { r: d[0], g: d[1], b: d[2] }
      }
      const lum = (c: { r: number; g: number; b: number }) => {
        const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4) }
        return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b)
      }
      const ratio = (a: { r: number; g: number; b: number }, b: { r: number; g: number; b: number }) =>
        (Math.max(lum(a), lum(b)) + 0.05) / (Math.min(lum(a), lum(b)) + 0.05)
      const canvas = px(rs.getPropertyValue('--bg-base'))
      return tones.map((t) => {
        const fill = px(rs.getPropertyValue(`--status-${t}`))
        const text = px(rs.getPropertyValue(`--status-${t}-text`))
        return {
          tone: t,
          fillOnCanvas: +ratio(fill, canvas).toFixed(2),
          fillVsText: +ratio(fill, text).toFixed(2),
        }
      })
    }, [...TONES])

    expect(got.length).toBe(3)
    // A run that resolved nothing would return three 1.00s and pass a
    // >= comparison written the other way round; naming the shape stops that.
    expect(got.map((g) => g.tone).sort()).toEqual(['danger', 'success', 'warning'])
    const weak = got
      .filter((g) => g.fillOnCanvas < 3)
      .map((g) => `--status-${g.tone} ${g.fillOnCanvas}:1 on --bg-base`)
    expect(weak, 'a status fill is no longer visible as a mark on the canvas').toEqual([])
  })
}
