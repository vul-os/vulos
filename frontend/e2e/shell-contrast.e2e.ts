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
 * No exceptions. Both entries that used to sit here are fixed:
 *
 *  - the "alpha" watermark was `var(--accent)` at opacity 0.85 and measured
 *    2.80:1. The accent is the USER's choice, so no palette value could fix it;
 *    it now uses --accent-text, derived at runtime from whatever they picked
 *    (core/accentContrast.ts).
 *  - the menu-bar date was Tailwind's `text-neutral-500` at 4.39:1 and is now
 *    neutral-400.
 *
 * If a shortfall ever has to be tolerated again, list it by its visible text so
 * a NEW failure cannot hide behind it — and say why it is a decision rather than
 * a defect.
 */

/**
 * Boot the shell in an explicit theme.
 *
 * The theme has to be PINNED. Left alone, this suite booted whatever the
 * resolved default happened to be — light, as it turned out — so a gate written
 * to protect "the desktop shell" was measuring half of it, and the dark theme
 * that most of this UI was designed against went unchecked. The marketing site
 * taught this the expensive way: a sweep run only against dark passed, and the
 * light theme turned out to have 576 sub-AA strings behind it.
 */
async function boot(page: Page, theme: 'dark' | 'light') {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  await installBackend(page)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.evaluate((t) => {
    document.documentElement.setAttribute('data-theme', t)
  }, theme)
  await page.waitForTimeout(2000)
}

for (const theme of ['dark', 'light'] as const) {
test(`desktop shell text meets WCAG AA on the ${theme} theme`, async ({ page }) => {
  test.setTimeout(120_000)
  await boot(page, theme)

  const all = await page.evaluate(SCAN)
  // A shell that failed to render would report nothing and pass.
  const measured = await page.evaluate(
    () => [...document.querySelectorAll('span,div,button,label')]
      .filter((e) => (e.textContent || '').trim() && !e.children.length).length,
  )
  expect(measured, 'the shell rendered almost no text, so this would pass vacuously').toBeGreaterThan(20)

  expect(
    all.map((f) => `${f.ratio} (need ${f.need}) ${f.color} on ${f.bg} ${f.size}px "${f.text}"`),
    'shell text below WCAG AA, measured on composited pixels',
  ).toEqual([])
})
}
