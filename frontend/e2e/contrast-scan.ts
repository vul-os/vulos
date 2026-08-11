import { expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

/**
 * Shared contrast measurement for the OS UI.
 *
 * Extracted because three separate throwaway probes measured the same thing
 * while nothing committed guarded it — the shell gate covered the shell, and the
 * fixes to Files, Calendar and Settings had no gate at all.
 *
 * Measured on COMPOSITED pixels, not on tokens, because the failure is never a
 * value in a file — it is a pair, and `opacity` is half of it. The desktop
 * watermark was the clearest case: `color: var(--accent)` looks fine in the
 * source and rendered at 2.80:1 because of an `opacity: 0.85` beside it. No
 * stylesheet audit can see that.
 */

/** Runs in the page: every rendered text node below its AA threshold. */
const SCAN = () => {
  // Resolve ANY CSS colour to sRGB by painting it, rather than parsing the
  // string.
  //
  // The previous version matched rgb()/rgba() with a regex and returned null for
  // anything else, which SILENTLY SKIPPED the element. Tailwind v4 emits oklch(),
  // and this theme's dark palette is oklch throughout — so every dark-theme
  // measurement quietly ignored those elements and reported clean. Contacts'
  // empty-state text failed in light at 2.42:1 and "passed" in dark purely
  // because its colour was oklch(0.439 0 0) and the parser could not read it.
  //
  // A 1x1 canvas resolves oklch, color(), hsl(), named colours and hex alike,
  // and it cannot go stale as CSS gains more colour syntaxes.
  const cv = document.createElement('canvas')
  cv.width = 1; cv.height = 1
  const ctx = cv.getContext('2d', { willReadFrequently: true })!
  const memo = new Map<string, { r: number; g: number; b: number; a: number } | null>()
  const parse = (c: string) => {
    if (memo.has(c)) return memo.get(c)!
    let out: { r: number; g: number; b: number; a: number } | null = null
    if (c && c !== 'none' && c !== 'transparent') {
      ctx.clearRect(0, 0, 1, 1)
      ctx.fillStyle = '#000'
      ctx.fillStyle = c
      // An unparseable value leaves fillStyle at the previous colour; painting
      // onto a cleared pixel and reading the alpha back is what distinguishes
      // "transparent" from "black".
      ctx.fillRect(0, 0, 1, 1)
      const d = ctx.getImageData(0, 0, 1, 1).data
      out = { r: d[0], g: d[1], b: d[2], a: d[3] / 255 }
    }
    memo.set(c, out)
    return out
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

  // PLACEHOLDERS are text the reader sees and this scan could not see: it walks
  // textContent, and a placeholder is an attribute. The mobile assistant's
  // "What do you need?" sat at 2.42:1 in plain sight while every gate reported
  // the shell clean.
  //
  // Measured through getComputedStyle(el, '::placeholder'), which resolves the
  // pseudo-element's own colour rather than the input's.
  for (const el of document.querySelectorAll('input[placeholder], textarea[placeholder]')) {
    const ph = (el as HTMLInputElement).placeholder
    if (!ph.trim() || (el as HTMLElement).offsetParent === null) continue
    if ((el as HTMLElement).closest('button:disabled, fieldset:disabled')) continue
    const cs = getComputedStyle(el)
    const fg = parse(getComputedStyle(el, '::placeholder').color)
    if (!fg) continue
    const op = chainOpacity(el)
    if (op < 0.05) continue
    const bg = bgOf(el)
    const eff = over({ ...fg, a: fg.a * op }, bg)
    const size = parseFloat(cs.fontSize)
    const weight = parseInt(cs.fontWeight) || 400
    const need = size >= 24 || (size >= 18.66 && weight >= 700) ? 3 : 4.5
    const got = ratio(eff, bg)
    if (got + 0.0001 < need) {
      out.push({
        text: 'placeholder: ' + ph.slice(0, 30),
        ratio: +got.toFixed(2),
        need,
        size,
        color: getComputedStyle(el, '::placeholder').color,
        bg: `rgb(${Math.round(bg.r)},${Math.round(bg.g)},${Math.round(bg.b)})`,
      })
    }
  }
  for (const el of document.querySelectorAll('p,li,td,th,a,span,div,small,button,label,h1,h2,h3,h4')) {
    const t = (el.textContent || '').trim()
    if (!t || el.children.length) continue
    if (el.closest('svg')) continue
    // WCAG 1.4.3 exempts INACTIVE user interface components, and a disabled
    // control dimmed to 30% is the standard way to show that state. Reporting it
    // would mean either failing forever or dimming disabled less than enabled,
    // which removes the signal. `disabled` only — an aria-disabled control that
    // still responds is not exempt, and neither is a merely unselected tab.
    if ((el as HTMLElement).closest('button:disabled, fieldset:disabled')) continue
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
 * Boot the shell in an EXPLICIT theme.
 *
 * Pinning is not a detail. Left to resolve on its own this suite booted light,
 * so a gate written to protect "the shell" measured half of it and the dark
 * theme — which most of this UI was designed against — went unchecked, carrying
 * --text-faint at 2.55:1. The marketing site taught the same lesson from the
 * other side: a dark-only sweep passed while light held 576 sub-AA strings.
 */
export async function bootShell(page: Page, theme: 'dark' | 'light'): Promise<void> {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  await installBackend(page)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
  await page.waitForTimeout(1800)
}

/** Launch a builtin through the command palette — the same path the Launchpad uses. */
export async function launchApp(page: Page, appName: string): Promise<void> {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(appName)
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
  await page.waitForTimeout(1800)
}

/** Every rendered text node below its WCAG AA threshold, as readable strings. */
export async function belowAA(page: Page): Promise<string[]> {
  const found = await page.evaluate(SCAN)
  return found.map(
    (f) => `${f.ratio} (need ${f.need}) ${f.color} on ${f.bg} ${f.size}px "${f.text}"`,
  )
}

/** How much text was measured — a screen that failed to render must not pass. */
export async function textNodeCount(page: Page): Promise<number> {
  return page.evaluate(
    () => [...document.querySelectorAll('span,div,button,label,p')]
      .filter((e) => (e.textContent || '').trim() && !e.children.length).length,
  )
}
