/**
 * shell-responsive-harness.ts — the measurement primitives for the shell's
 * responsive sweep, shared by the survey that produced the inventory and by the
 * gate that keeps it from rotting.
 *
 * Everything here MEASURES the composited box. Nothing reads a stylesheet, a
 * class name or a media query, because the defects this sweep found were all of
 * the form "the rule is written down and the pixel disagrees" — a `font-size`
 * token that is legal at 1280 and computes below the floor at 360, a dock whose
 * declared height is fine and whose safe-area padding pushes it over the
 * viewport, a control that is 44px tall in the stylesheet and 28px on screen
 * because a flex parent shrank it.
 */
import { expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

/** This suite's minimum legible rendered type size. */
export const MIN_FONT_PX = 12
/** The touch floor every platform guideline agrees on. */
export const TOUCH_FLOOR = 44

export interface Viewport {
  /** Human label used in every failure message. */
  label: string
  width: number
  height: number
  /** A finger, not a mouse. Drives `pointer: coarse` / `hover: none`. */
  touch: boolean
}

/**
 * The sweep's widths.
 *
 * Chosen to land ON and AROUND every structural boundary the shell has
 * (MOBILE_BREAKPOINT 768, TOUCH_STACK_MAX 1024) as well as in the middle of each
 * range, and to include BOTH orientations of every touch device — a phone in
 * landscape is 844×390, which is wider than the mobile breakpoint and only 390px
 * TALL, and no portrait-only sweep can produce that state.
 */
export const PHONE_PORTRAIT: Viewport[] = [
  { label: 'phone 320 portrait', width: 320, height: 568, touch: true },
  { label: 'phone 360 portrait', width: 360, height: 800, touch: true },
  { label: 'phone 390 portrait', width: 390, height: 844, touch: true },
  { label: 'phone 430 portrait', width: 430, height: 932, touch: true },
]

export const PHONE_LANDSCAPE: Viewport[] = [
  { label: 'phone 568 landscape', width: 568, height: 320, touch: true },
  { label: 'phone 740 landscape', width: 740, height: 360, touch: true },
  { label: 'phone 844 landscape', width: 844, height: 390, touch: true },
  { label: 'phone 932 landscape', width: 932, height: 430, touch: true },
]

export const TABLET: Viewport[] = [
  { label: 'tablet 768 portrait', width: 768, height: 1024, touch: true },
  { label: 'tablet 834 portrait', width: 834, height: 1194, touch: true },
  { label: 'tablet 1024 portrait', width: 1024, height: 1366, touch: true },
  { label: 'tablet 1024 landscape', width: 1024, height: 768, touch: true },
  { label: 'tablet 1194 landscape', width: 1194, height: 834, touch: true },
  { label: 'tablet 1366 landscape', width: 1366, height: 1024, touch: true },
]

/** A pointer-driven desktop, including a window dragged narrow. */
export const DESKTOP: Viewport[] = [
  { label: 'desktop 1440', width: 1440, height: 900, touch: false },
  { label: 'desktop 1280', width: 1280, height: 800, touch: false },
  { label: 'desktop 1024', width: 1024, height: 700, touch: false },
  { label: 'desktop 900 resized', width: 900, height: 600, touch: false },
  { label: 'desktop 800 resized', width: 800, height: 520, touch: false },
  { label: 'desktop 768 resized', width: 768, height: 480, touch: false },
  { label: 'desktop 700 resized', width: 700, height: 440, touch: false },
]

export const ALL_VIEWPORTS = [...PHONE_PORTRAIT, ...PHONE_LANDSCAPE, ...TABLET, ...DESKTOP]

/**
 * Stub the telemetry socket so SystemPulse's battery control actually renders.
 *
 * Lifted from mobile-touch-targets.e2e.ts, and for the reason recorded there: a
 * `page.route` override of /api/system/stats is INERT because the shell reads a
 * WebSocket, so a sweep without this measures one control fewer than it claims
 * to and reports a clean pass for a surface that never rendered.
 */
export async function stubTelemetry(page: Page) {
  await page.addInitScript(() => {
    const FRAME = JSON.stringify({ battery: 62, charging: false, temp: 41, uptime: '3d 4h', cpu: 12, mem: 44 })
    class FakeSocket {
      onopen: (() => void) | null = null
      onmessage: ((e: { data: string }) => void) | null = null
      onclose: (() => void) | null = null
      onerror: (() => void) | null = null
      readyState = 1
      constructor(url: string) {
        if (!String(url).includes('/api/telemetry')) return
        setTimeout(() => { this.onopen?.(); this.onmessage?.({ data: FRAME }) }, 0)
      }
      close() { this.readyState = 3 }
      send() { /* the shell never sends on this socket */ }
    }
    ;(window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket
  })
}

/**
 * Boot the shell at a viewport and wait for the shell chrome to exist.
 *
 * Waits on EITHER shell's navigation surface, because which one mounts is
 * exactly what this sweep is measuring; waiting on the mobile dock alone would
 * hang for 25s on every desktop width and waiting on the desktop dock alone
 * would hang on every phone.
 */
export async function bootShell(page: Page, vp: Viewport) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.setViewportSize({ width: vp.width, height: vp.height })
  await stubTelemetry(page)
  await installBackend(page)
  await page.goto('/')
  await expect(
    page.locator('nav[aria-label="System navigation"], .vshell-bar, [role="toolbar"][aria-label="Dock"]').first(),
  ).toBeVisible({ timeout: 25_000 })
  // The shell settles a frame after mount (dock geometry reads its own box).
  await page.waitForTimeout(350)
  return errors
}

/** Which shell mounted, as the DOM reports it rather than as a width implies. */
export async function shellKind(page: Page): Promise<'mobile' | 'desktop'> {
  return page.evaluate(() =>
    document.querySelector('[data-shell="mobile"], [data-mobile-home], nav[aria-label="System navigation"]')
      ? 'mobile' : 'desktop',
  )
}

/** Horizontal spill of the document itself, in px. >1 is a horizontal scrollbar. */
export async function docSpill(page: Page): Promise<number> {
  return page.evaluate(() => Math.max(
    document.documentElement.scrollWidth - document.documentElement.clientWidth,
    document.body.scrollWidth - document.documentElement.clientWidth,
  ))
}

export interface Spilling { sel: string; right: number; left: number; w: number }

/**
 * Elements painted outside the viewport horizontally.
 *
 * The document can measure clean while an individual element hangs off the
 * edge — `overflow: hidden` on an ancestor clips the scroll extent without
 * moving the child back, so the content is genuinely unreachable and
 * `scrollWidth` says everything is fine. Both checks are needed; neither
 * subsumes the other.
 *
 * Elements inside a horizontal SCROLLER are exempt: a chip rail that scrolls is
 * a design, not a defect. Position-fixed offscreen panels (a closed sheet parked
 * at translateX(-100%)) are exempt for the same reason — they are not content
 * the user is being denied, they are content that has not been asked for.
 */
export async function spillingElements(page: Page, root = 'body'): Promise<Spilling[]> {
  return page.evaluate((rootSel) => {
    const host = document.querySelector(rootSel)
    if (!host) return []
    const out: Spilling[] = []
    const W = document.documentElement.clientWidth
    const scrollerCache = new WeakMap<Element, boolean>()
    const inScroller = (el: Element): boolean => {
      let p: Element | null = el.parentElement
      while (p && p !== document.documentElement) {
        let v = scrollerCache.get(p)
        if (v === undefined) {
          const cs = getComputedStyle(p)
          v = /auto|scroll/.test(cs.overflowX) || cs.overflow === 'auto' || cs.overflow === 'scroll'
          scrollerCache.set(p, v)
        }
        if (v) return true
        p = p.parentElement
      }
      return false
    }
    const named = (el: Element) => {
      const id = el.id ? `#${el.id}` : ''
      const cls = typeof el.className === 'string' && el.className
        ? '.' + el.className.trim().split(/\s+/).slice(0, 3).join('.') : ''
      return `${el.tagName.toLowerCase()}${id}${cls}`.slice(0, 90)
    }
    for (const el of host.querySelectorAll('*')) {
      const cs = getComputedStyle(el)
      if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') continue
      const r = el.getBoundingClientRect()
      if (r.width < 1 || r.height < 1) continue
      const right = Math.round(r.right - W)
      const left = Math.round(-r.left)
      if (right <= 1 && left <= 1) continue
      if (inScroller(el)) continue
      // A fixed element parked entirely offscreen is a closed panel.
      if (cs.position === 'fixed' && (r.right <= 0 || r.left >= W)) continue
      out.push({ sel: named(el), right, left, w: Math.round(r.width) })
    }
    return out
  }, root)
}

export interface Tiny { sel: string; px: number; text: string }

/**
 * Rendered text below the type floor.
 *
 * Leaf elements only, and only ones that actually paint a box with visible text,
 * because a `font-size: 10px` on an empty wrapper harms nobody and a gate that
 * fails on one teaches people to widen the exemption list.
 */
export async function tinyText(page: Page, floor = MIN_FONT_PX, roots?: string[]): Promise<Tiny[]> {
  return page.evaluate(([min, rootSelectors]) => {
    const out: Tiny[] = []
    const named = (el: Element) => {
      const cls = typeof el.className === 'string' && el.className
        ? '.' + el.className.trim().split(/\s+/).slice(0, 3).join('.') : ''
      return `${el.tagName.toLowerCase()}${cls}`.slice(0, 90)
    }
    const scope = (rootSelectors as string[] | undefined)?.length
      ? (rootSelectors as string[]).flatMap((s) => [...document.querySelectorAll(s)])
      : [document.body]
    const seen = new Set<Element>()
    const pool: Element[] = []
    for (const host of scope) {
      for (const el of host.querySelectorAll('*')) {
        if (seen.has(el)) continue
        seen.add(el)
        pool.push(el)
      }
    }
    for (const el of pool) {
      if (el.children.length) continue
      const text = (el.textContent || '').trim()
      if (!text) continue
      const cs = getComputedStyle(el)
      if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') continue
      const r = el.getBoundingClientRect()
      if (r.width < 1 || r.height < 1) continue
      const px = parseFloat(cs.fontSize)
      if (!(px < min)) continue
      out.push({ sel: named(el), px: Math.round(px * 100) / 100, text: text.slice(0, 40) })
    }
    return out
  }, [floor, roots] as const)
}

/** How many text nodes actually painted — the vacuity guard for tinyText(). */
export async function paintedTextNodes(page: Page): Promise<number> {
  return page.evaluate(() => [...document.querySelectorAll('*')].filter(
    (n) => !n.children.length && (n.textContent || '').trim() && n.getBoundingClientRect().width > 0,
  ).length)
}

export interface Small { root: string; label: string; w: number; h: number }

/** Interactive controls under the touch floor, scoped to named roots. */
export async function smallTargets(page: Page, roots: string[], floor = TOUCH_FLOOR): Promise<Small[]> {
  return page.evaluate(([rootSelectors, min]) => {
    const bad: Small[] = []
    const SEL = 'button, a[href], [role="button"], [role="tab"], input[type="checkbox"], select'
    for (const root of rootSelectors as string[]) {
      for (const host of document.querySelectorAll(root)) {
        for (const el of host.querySelectorAll(SEL)) {
          const cs = getComputedStyle(el)
          if (cs.display === 'none' || cs.visibility === 'hidden') continue
          const b = el.getBoundingClientRect()
          if (b.width === 0 || b.height === 0) continue
          if (b.width < (min as number) || b.height < (min as number)) {
            bad.push({
              root,
              label: (el.getAttribute('aria-label') || el.textContent || el.className || '?')
                .toString().trim().slice(0, 44),
              w: Math.round(b.width * 10) / 10,
              h: Math.round(b.height * 10) / 10,
            })
          }
        }
      }
    }
    return bad
  }, [roots, floor] as const)
}

/**
 * What each check actually looked at.
 *
 * Every empty result in this file — no spill, no tiny text, no small target —
 * is indistinguishable from "the surface never rendered and there was nothing
 * to measure". This suite has shipped that exact pass more than once (see
 * mobile-touch-targets.e2e.ts's telemetry stub, and apphub-responsive's
 * `painted` guard). These are the denominators, asserted alongside the
 * numerators so a green result carries evidence that it was earned.
 */
export interface Scanned {
  /** Elements the overflow check considered. */
  elements: number
  /** Leaf elements with visible text the type floor measured. */
  textNodes: number
  /** Interactive controls found inside the named roots. */
  controls: number
}

export async function scanned(page: Page, roots: string[]): Promise<Scanned> {
  return page.evaluate((rootSelectors) => {
    const SEL = 'button, a[href], [role="button"], [role="tab"], input[type="checkbox"], select'
    let controls = 0
    for (const root of rootSelectors) {
      for (const host of document.querySelectorAll(root)) {
        for (const el of host.querySelectorAll(SEL)) {
          const b = el.getBoundingClientRect()
          if (b.width > 0 && b.height > 0) controls++
        }
      }
    }
    // Scoped to the SAME roots the type floor is scoped to, so this is the
    // denominator of that check and not of some larger page.
    let textNodes = 0
    const seen = new Set<Element>()
    for (const root of rootSelectors) {
      for (const host of document.querySelectorAll(root)) {
        for (const el of host.querySelectorAll('*')) {
          if (seen.has(el)) continue
          seen.add(el)
          if (el.children.length) continue
          if (!(el.textContent || '').trim()) continue
          const r = el.getBoundingClientRect()
          if (r.width >= 1 && r.height >= 1) textNodes++
        }
      }
    }
    return { elements: document.querySelectorAll('body *').length, textNodes, controls }
  }, roots)
}

/** How much of the viewport HEIGHT the shell's own chrome consumes, as a fraction. */
export async function chromeShare(page: Page): Promise<{ px: number; frac: number; parts: Record<string, number> }> {
  return page.evaluate(() => {
    const parts: Record<string, number> = {}
    let px = 0
    for (const sel of ['.vmob-bar', 'nav[aria-label="System navigation"]', '.vshell-bar', '.vdock-layer']) {
      const el = document.querySelector(sel) as HTMLElement | null
      if (!el) continue
      const cs = getComputedStyle(el)
      if (cs.display === 'none' || cs.visibility === 'hidden') continue
      const r = el.getBoundingClientRect()
      // Only chrome that is actually IN the viewport eats the viewport.
      const h = Math.max(0, Math.min(r.bottom, window.innerHeight) - Math.max(r.top, 0))
      if (h < 1) continue
      parts[sel] = Math.round(h)
      px += h
    }
    return { px: Math.round(px), frac: Math.round((px / window.innerHeight) * 1000) / 1000, parts }
  })
}
