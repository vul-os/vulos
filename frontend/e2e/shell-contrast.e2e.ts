import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

/**
 * Text in the desktop shell meets WCAG AA against what is actually behind it.
 *
 * Measured on COMPOSITED pixels, not on tokens, because the failure is never a
 * value in a file — it is a pair, and `opacity` is half of it. The desktop
 * watermark below is the clearest case: `color: var(--accent)` looks fine in the
 * source and renders at 2.80:1 because of an `opacity: 0.85` beside it. No
 * stylesheet audit can see that.
 *
 * Scoped to the shell as it boots. It is not a claim that the whole OS meets AA —
 * app windows, dialogs and settings panes are not visited here.
 */

/** Runs in the page: every rendered text node below its AA threshold. */
const SCAN = () => {
  const parse = (c: string) => {
    const m = /rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/.exec(c)
    return m ? { r: +m[1], g: +m[2], b: +m[3], a: m[4] === undefined ? 1 : +m[4] } : null
  }
  type C = { r: number; g: number; b: number; a: number }
  const over = (f: C, g: C): C => ({
    r: f.r * f.a + g.r * (1 - f.a),
    g: f.g * f.a + g.g * (1 - f.a),
    b: f.b * f.a + g.b * (1 - f.a),
    a: 1,
  })
  const lum = (c: C) => {
    const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4) }
    return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b)
  }
  const ratio = (a: C, b: C) => {
    const L1 = lum(a), L2 = lum(b)
    return (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05)
  }
  // Opacity multiplies down the ancestor chain and appears in no token.
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

  const out: { text: string; ratio: number; need: number; color: string; bg: string; size: number }[] = []
  for (const el of document.querySelectorAll('p,li,td,th,a,span,div,small,button,label,h1,h2,h3,h4')) {
    const t = (el.textContent || '').trim()
    if (!t || el.children.length) continue
    if (el.closest('svg')) continue
    if (!(el as HTMLElement).offsetParent) continue
    const r = el.getBoundingClientRect()
    if (r.width < 2 || r.height < 2) continue
    const cs = getComputedStyle(el)
    const fg = parse(cs.color)
    if (!fg) continue
    const op = chainOpacity(el)
    if (op < 0.05) continue // not painted
    const bg = bgOf(el)
    const eff = over({ ...fg, a: fg.a * op }, bg)
    const size = parseFloat(cs.fontSize)
    const weight = parseInt(cs.fontWeight) || 400
    const need = size >= 24 || (size >= 18.66 && weight >= 700) ? 3 : 4.5
    const got = ratio(eff, bg)
    if (got + 0.0001 < need) {
      out.push({
        text: t.slice(0, 40), ratio: +got.toFixed(2), need, size,
        color: cs.color, bg: `rgb(${Math.round(bg.r)},${Math.round(bg.g)},${Math.round(bg.b)})`,
      })
    }
  }
  return out
}

/**
 * Two known shortfalls, each a design decision rather than a defect I can fix by
 * picking a number. Listed by their visible text so a NEW failure cannot hide
 * behind them.
 *
 *  - "alpha": the empty-desktop watermark, `var(--accent)` at opacity 0.85. The
 *    accent is set at RUNTIME from the user's preference (ThemeProvider applies
 *    it to documentElement, DEFAULT_ACCENT = #3b82f6), so no static value makes
 *    this safe — a user may pick any colour. Fixing it properly means not using
 *    a user-chosen accent for small text, which is a product decision.
 *  - the calendar date: Tailwind's `text-neutral-500` (#737373) at 4.39:1 — a 2%
 *    shortfall. The fix is either repalleting neutral-500 for the whole UI or
 *    moving these components onto the semantic --text-* tokens; both are wider
 *    than this test.
 */
const KNOWN = [/^alpha$/, /^\w{3}, \w{3} \d+$/]

async function boot(page: Page) {
  await installBackend(page)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.waitForTimeout(2000)
}

test('desktop shell text meets WCAG AA', async ({ page }) => {
  test.setTimeout(120_000)
  await boot(page)

  const all = await page.evaluate(SCAN)
  // A shell that failed to render would report nothing and pass.
  const measured = await page.evaluate(
    () => [...document.querySelectorAll('span,div,button,label')]
      .filter((e) => (e.textContent || '').trim() && !e.children.length).length,
  )
  expect(measured, 'the shell rendered almost no text, so this would pass vacuously').toBeGreaterThan(20)

  const unexpected = all.filter((f) => !KNOWN.some((re) => re.test(f.text)))
  expect(
    unexpected.map((f) => `${f.ratio} (need ${f.need}) ${f.color} on ${f.bg} ${f.size}px "${f.text}"`),
    'shell text below WCAG AA, measured on composited pixels',
  ).toEqual([])
})
