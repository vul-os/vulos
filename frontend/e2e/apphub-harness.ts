import { expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { APPS, INSTALLED, type FixtureApp } from './apphub-fixture'

/**
 * Shared boot + measurement for the App Hub specs.
 *
 * The hub is a WINDOW, and that is the whole reason this file exists. Every
 * other visual gate in this suite measures something whose width is the
 * viewport's width, so `page.setViewportSize` is a complete description of the
 * layout under test. The hub's width is the width of a frame the user drags,
 * which on a 1440px screen can be 400px — a combination no viewport size can
 * produce and therefore one no viewport-driven gate can see.
 *
 * It was not a hypothetical. Before this wave the grid was `md:grid-cols-2
 * xl:grid-cols-3`, viewport media queries to a one, so a 442px window on a
 * 1440px screen laid out THREE columns inside it: cards 50px wide, every name
 * clipped to nothing, and the third column rendered outside the window frame —
 * 107px of horizontal overflow, measured. Maximised at 1440 the same code looked
 * correct, which is why it survived.
 */

export const HUB_BACKEND = {
  'GET /api/store/registry': json(APPS),
  'GET /api/store/installed': json(INSTALLED),
  'GET /api/packages/cache': json({ ready: true, arch: 'amd64' }),
}

/** HUB_BACKEND with a different registry payload (empty, huge, failing, …). */
export function hubBackend(
  apps: FixtureApp[] | unknown,
  opts: { installed?: unknown; cache?: unknown } = {},
): Record<string, unknown> {
  return {
    'GET /api/store/registry': json(apps),
    'GET /api/store/installed': json(opts.installed ?? []),
    'GET /api/packages/cache': json(opts.cache ?? { ready: true, arch: 'amd64' }),
  }
}

/** The hub's own root element. Selected by a stable data attribute, not by a
 *  class list — a Tailwind class string is churn, and a gate keyed on one
 *  silently stops finding its subject the first time a utility is reordered. */
export const HUB = '[data-app="apphub"]'

export async function bootShell(
  page: Page,
  theme: 'dark' | 'light',
  size: { width: number; height: number },
  backend: Record<string, unknown>,
): Promise<void> {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t as string) } catch { /* private mode */ }
    document.documentElement.setAttribute('data-theme', t as string)
  }, theme)
  await page.setViewportSize(size)
  await installBackend(page, backend)
  await page.goto('/')
  // Whichever shell this width selects — desktop menu bar or phone nav.
  const marker = size.width < 768
    ? page.locator('nav[aria-label="System navigation"]')
    : page.getByTitle('Applications')
  await expect(marker).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
}

/** Open the App Hub through the command palette — the path a user takes. */
export async function launchHub(page: Page): Promise<void> {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1500 })
  }).toPass({ timeout: 20_000 })
  await input.fill('App Hub')
  await expect(page.getByText('App Hub', { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page.locator(HUB)).toBeVisible({ timeout: 15_000 })
  await page.locator(`${HUB} [data-hub-ready="1"]`).waitFor({ timeout: 15_000 })
}

export interface BootOptions {
  theme?: 'dark' | 'light'
  width?: number
  height?: number
  backend?: Record<string, unknown>
  /** Desktop only: fill the screen, so the hub's width IS the viewport width. */
  maximize?: boolean
}

/**
 * Boot the shell and open the hub. Returns the hub's measured width, which is
 * what every geometry assertion should be phrased against — not the viewport.
 */
export async function bootHub(page: Page, opts: BootOptions = {}): Promise<number> {
  const width = opts.width ?? 1440
  const height = opts.height ?? 900
  await bootShell(page, opts.theme ?? 'dark', { width, height }, opts.backend ?? HUB_BACKEND)
  await openHubHere(page, width, opts.maximize ?? true)
  return hubWidth(page)
}

/**
 * Open the hub in an ALREADY-BOOTED shell.
 *
 * Split out of bootHub so a spec can measure the shell BEFORE the hub exists.
 * The contrast gate needs exactly that: the phone shell carries a sub-AA dock
 * badge that belongs to the mobile layout, not to the hub, and a whole-document
 * scan cannot tell the two apart. Measuring before and after and comparing the
 * two sets attributes each failure to whatever actually introduced it, using
 * the one shared scanner rather than a second, divergent copy of it.
 */
export async function openHubHere(page: Page, width: number, maximize = true): Promise<void> {
  await launchHub(page)
  if (width >= 768 && maximize) {
    await page.getByLabel('Maximize window').first().click()
  }
  await settle(page)
}

/**
 * Boot at a full-size viewport, then DRAG the window's resize grip until the
 * hub is `target` px wide. The narrow-window case, driven the way a user
 * produces it, on a screen wide enough that every desktop media query is still
 * matching.
 */
export async function bootHubDraggedTo(
  page: Page,
  target: number,
  opts: BootOptions = {},
): Promise<number> {
  const width = opts.width ?? 1440
  const height = opts.height ?? 900
  await bootShell(page, opts.theme ?? 'dark', { width, height }, opts.backend ?? HUB_BACKEND)
  await launchHub(page)

  const grip = page.locator('.vwin-resize').first()
  const frame = page.locator('.vwin').first()
  const gb = await grip.boundingBox()
  const fb = await frame.boundingBox()
  if (!gb || !fb) throw new Error('apphub-harness: no window frame or resize grip to drag')
  await page.mouse.move(gb.x + gb.width / 2, gb.y + gb.height / 2)
  await page.mouse.down()
  await page.mouse.move(fb.x + target, gb.y + gb.height / 2, { steps: 14 })
  await page.mouse.up()
  await settle(page)

  const got = await hubWidth(page)
  // The window enforces a minimum size; a drag that silently stopped short
  // would leave the spec measuring a wide layout while claiming a narrow one.
  expect(
    Math.abs(got - target),
    `asked for a ${target}px hub, the drag produced ${got}px — this spec would ` +
      'be measuring a width it did not set',
  ).toBeLessThanOrEqual(24)
  return got
}

/** Wait for layout + animations to stop, so measurements are of a resting UI. */
export async function settle(page: Page): Promise<void> {
  await page.waitForFunction(
    () => document.getAnimations().every((a) => a.playState === 'finished' || a.playState === 'running' && (a as CSSAnimation).animationName === 'spin'),
    null,
    { timeout: 10_000 },
  ).catch(() => { /* a perpetual spinner must not hang the suite */ })
  await page.waitForTimeout(450)
}

export async function hubWidth(page: Page): Promise<number> {
  return page.locator(HUB).evaluate((el) => Math.round(el.getBoundingClientRect().width))
}

export interface Overflowing {
  tag: string
  cls: string
  text: string
  overflow: number
  width: number
}

/**
 * Every element inside the hub whose content is wider than its box.
 *
 * Three exclusions, each for a reason that would otherwise make this useless:
 *
 *  - `overflow-x: auto|scroll` — a deliberate scroller. Its content is SUPPOSED
 *    to exceed its box.
 *  - `text-overflow: ellipsis` — a truncating line legitimately has
 *    scrollWidth > clientWidth; that IS truncation working. Measured on the old
 *    hub, every one of these was a `truncate` description, so an unfiltered
 *    check reports 1200px of "overflow" on a layout that is behaving.
 *  - `overflow-x: clip|hidden` — the KNOWN TRAP in this repo. scrollWidth is
 *    pinned to clientWidth under clip, so an overflowing child reports zero and
 *    the check passes on the exact defect it was written for. Those elements are
 *    measured through their CHILDREN's bounding boxes instead (see
 *    childrenEscaping).
 */
export async function overflowing(page: Page, root = HUB): Promise<Overflowing[]> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel)
    if (!el) throw new Error(`overflowing(): no element matches ${sel}`)
    const out: Overflowing[] = []
    for (const n of [el, ...el.querySelectorAll('*')] as HTMLElement[]) {
      const r = n.getBoundingClientRect()
      if (r.width < 1 || r.height < 1) continue
      const cs = getComputedStyle(n)
      if (cs.overflowX === 'auto' || cs.overflowX === 'scroll') continue
      if (cs.overflowX === 'clip' || cs.overflowX === 'hidden') continue
      if (cs.textOverflow === 'ellipsis') continue
      const ov = n.scrollWidth - n.clientWidth
      if (ov > 1) {
        out.push({
          tag: n.tagName,
          cls: String(n.className).slice(0, 60),
          text: (n.textContent || '').trim().slice(0, 40),
          overflow: ov,
          width: Math.round(r.width),
        })
      }
    }
    return out
  }, root)
}

/**
 * Children whose painted box sticks out past their clipping ancestor.
 *
 * This is the half `overflowing()` cannot see. Under `overflow-x: clip` the
 * parent's scrollWidth equals its clientWidth no matter how far the child
 * escapes, so the only honest measurement is the child's own rectangle against
 * the container's. On the pre-wave hub this is what caught the third grid
 * column rendering outside the window frame.
 *
 * ── The exclusion that makes this usable ────────────────────────────────────
 *
 * Anything inside a horizontal SCROLLER is skipped. The hub's filter chips live
 * in an `overflow-x: auto` rail, so the off-screen chips are not escaping
 * anything — being wider than the viewport and scrollable is the entire design.
 * Without this, the first run of this helper reported 15 "spills" at every
 * width below 700px, on a rail that was working exactly as intended: a gate
 * that fires on correct behaviour teaches people to ignore it, and the real
 * defect it was written for hides in the noise.
 *
 * Note that the scroller's OWN box is still checked — a chip rail that itself
 * escaped the hub would still be reported.
 */
export async function childrenEscaping(page: Page, root = HUB): Promise<string[]> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel) as HTMLElement | null
    if (!el) throw new Error(`childrenEscaping(): no element matches ${sel}`)
    const box = el.getBoundingClientRect()
    const out: string[] = []

    /** Is some ancestor between `n` (exclusive) and the root a horizontal scroller? */
    const insideScroller = (n: HTMLElement): boolean => {
      let a = n.parentElement
      while (a && a !== el) {
        const ox = getComputedStyle(a).overflowX
        if (ox === 'auto' || ox === 'scroll') return true
        a = a.parentElement
      }
      return false
    }

    for (const n of el.querySelectorAll<HTMLElement>('*')) {
      const cs = getComputedStyle(n)
      if (cs.position === 'fixed') continue
      const r = n.getBoundingClientRect()
      if (r.width < 1 || r.height < 1) continue
      if (insideScroller(n)) continue
      const spill = Math.max(box.left - r.left, r.right - box.right)
      if (spill > 1) {
        out.push(
          `${n.tagName}.${String(n.className).slice(0, 44)} spills ${Math.round(spill)}px ` +
            `"${(n.textContent || '').trim().slice(0, 24)}"`,
        )
      }
    }
    return out
  }, root)
}

/** Rendered font sizes of the hub's own text, smallest first. */
export async function fontSizes(page: Page, root = HUB): Promise<number[]> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel)
    if (!el) throw new Error(`fontSizes(): no element matches ${sel}`)
    const sizes = new Set<number>()
    for (const n of el.querySelectorAll<HTMLElement>('*')) {
      if (!(n.textContent || '').trim() || n.children.length) continue
      if (n.closest('svg')) continue
      const r = n.getBoundingClientRect()
      if (r.width < 1 || r.height < 1) continue
      sizes.add(Math.round(parseFloat(getComputedStyle(n).fontSize) * 100) / 100)
    }
    return [...sizes].sort((a, b) => a - b)
  }, root)
}

/**
 * Interactive targets whose EFFECTIVE hit area is smaller than `min`, as
 * readable strings.
 *
 * "Effective" is the whole point. The card's app-name button is 20px tall and
 * is nonetheless the easiest thing in the hub to hit, because it carries a
 * `::after { position: absolute; inset: 0 }` that stretches its hit area over
 * the entire card — the standard way to make a whole card clickable without
 * nesting a button inside a button. Measuring `getBoundingClientRect()` alone
 * reported all thirty of them as 20px failures at every width, which is a gate
 * that fails on correct code.
 *
 * So where a control has a stretched absolutely-positioned pseudo-element, the
 * measurement is taken from the box that pseudo-element actually covers — its
 * offsetParent, the nearest positioned ancestor. Remove the `::after` and each
 * name reverts to a 20px target and this reports it, which is what makes the
 * exemption a measurement rather than a blanket excuse.
 */
export async function smallTargets(page: Page, min = 24, root = HUB): Promise<string[]> {
  return page.evaluate(([sel, m]) => {
    const el = document.querySelector(sel as string)
    if (!el) throw new Error('smallTargets(): hub not found')
    const out: string[] = []

    /** The rect a click on this control can actually land in. */
    const hitRect = (n: HTMLElement): DOMRect => {
      for (const pseudo of ['::after', '::before']) {
        const cs = getComputedStyle(n, pseudo)
        if (cs.content === 'none' || cs.position !== 'absolute') continue
        // inset:0 resolves to four computed lengths of 0px.
        if (cs.top !== '0px' || cs.right !== '0px' || cs.bottom !== '0px' || cs.left !== '0px') continue
        const host = n.offsetParent as HTMLElement | null
        if (host) return host.getBoundingClientRect()
      }
      return n.getBoundingClientRect()
    }

    for (const n of el.querySelectorAll<HTMLElement>('button, a[href], input, [role="button"], [role="tab"], [role="option"]')) {
      const box = n.getBoundingClientRect()
      if (box.width < 1 || box.height < 1) continue
      const r = hitRect(n)
      if (r.height + 0.5 < (m as number) || r.width + 0.5 < (m as number)) {
        out.push(`${n.tagName} ${Math.round(r.width)}x${Math.round(r.height)} "${(n.textContent || n.getAttribute('aria-label') || '').trim().slice(0, 28)}"`)
      }
    }
    return out
  }, [root, min] as const)
}
