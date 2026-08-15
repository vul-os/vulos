/**
 * mobile-touch-targets.e2e.ts — the 44px floor across the whole phone chrome.
 *
 * The mobile workstream took the phone from 51 sub-44px targets to 5 and then
 * scoped its own gate to the surfaces it owned, listing the rest rather than
 * folding them in — because a floor that quietly excludes the failures is not a
 * floor. This one is scoped to the CHROME: the status bar, the dock, the home
 * screen and the switcher, everything the shell itself draws and that is on
 * screen on every boot. It names the offenders it finds, with their measured
 * sizes, so a regression reports WHAT shrank rather than just that something did.
 *
 * Apps rendered INSIDE the shell are deliberately out of scope here — they are
 * gated per app (see e2e/mobile-files.e2e.ts for the file manager) so that one
 * unfixed builtin cannot hold the shell's own floor hostage.
 */
import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'
import { mkdirSync } from 'node:fs'

const SHOTS = 'test-results/mobile-touch'
mkdirSync(SHOTS, { recursive: true })

/** 44px is the platform floor every touch guideline agrees on. WCAG 2.5.8's
 *  normative minimum is 24px; this is the stricter, intended one. */
const FLOOR = 44

const CHROME_ROOTS = [
  '.vmob-bar',                            // status bar: TrustBadge + SystemPulse
  'nav[aria-label="System navigation"]',  // the dock
  '[data-mobile-home]',                   // the home screen
  '[data-mobile-switcher]',               // the app switcher
]

/**
 * Make SystemPulse's BATTERY control actually render.
 *
 * The first version of this helper overrode `GET /api/system/stats` and looked
 * entirely reasonable. core/useTelemetry.ts reads a WEBSOCKET (`/api/telemetry`)
 * and no such route exists, so the override was inert, the battery control never
 * rendered, and the floor silently never measured it — a gate passing because
 * its subject was absent, which is the exact failure mode this repository keeps
 * finding in its own guards.
 *
 * So the socket is stubbed for real: a minimal WebSocket stand-in that opens and
 * delivers one telemetry frame. `battery` present is what gates the control in
 * SystemPulse's compact branch.
 */
async function stubTelemetry(page: Page) {
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
        setTimeout(() => {
          this.onopen?.()
          this.onmessage?.({ data: FRAME })
        }, 0)
      }
      close() { this.readyState = 3 }
      send() { /* the shell never sends on this socket */ }
    }
    ;(window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket
  })
}

async function boot(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await stubTelemetry(page)
  await installBackend(page)
  await page.goto('/')
  await expect(page.locator('nav[aria-label="System navigation"]')).toBeVisible({ timeout: 25_000 })
  // The battery control is the proof the stub took. Without it this suite would
  // be measuring one control fewer than it claims to.
  await expect(page.getByRole('button', { name: 'Battery' })).toBeVisible({ timeout: 15_000 })
  return errors
}

interface Small { root: string; label: string; w: number; h: number }

async function smallTargets(page: Page, roots: string[]): Promise<Small[]> {
  return page.evaluate((rootSelectors) => {
    const bad: Small[] = []
    for (const root of rootSelectors) {
      for (const host of document.querySelectorAll(root)) {
        for (const el of host.querySelectorAll('button, a[href], [role="button"], input[type="checkbox"]')) {
          const b = el.getBoundingClientRect()
          // A zero box is a hidden control, not a small one.
          if (b.width === 0 || b.height === 0) continue
          if (b.width < 44 || b.height < 44) {
            bad.push({
              root,
              label: (el.getAttribute('aria-label') || el.textContent || el.className || '?').toString().trim().slice(0, 48),
              w: Math.round(b.width * 10) / 10,
              h: Math.round(b.height * 10) / 10,
            })
          }
        }
      }
    }
    return bad
  }, roots) as Promise<Small[]>
}

test.describe('phone 390×844 (touch)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  test('nothing in the shell chrome is under 44px', async ({ page }) => {
    const errors = await boot(page)
    const small = await smallTargets(page, CHROME_ROOTS)
    await page.screenshot({ path: `${SHOTS}/phone-390-chrome.png` })
    expect(small, `sub-${FLOOR}px targets in the shell chrome: ${JSON.stringify(small, null, 2)}`).toEqual([])
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('the trust badge is bigger, and still says what it says', async ({ page }) => {
    // TrustBadge is a TRUST surface: the customization model's security argument
    // turns on it being unspoofable AND readable. Growing the hit area must not
    // be paid for by shrinking or hiding what it reports, so this asserts the
    // signals are present and the badge is not merely a bigger blank pill.
    await boot(page)
    const badge = page.getByRole('button', { name: 'Sovereignty and privacy status' })
    await expect(badge).toBeVisible()
    const box = await badge.boundingBox()
    if (!box) throw new Error('the trust badge has no bounding box')
    expect(box.width).toBeGreaterThanOrEqual(FLOOR)
    expect(box.height).toBeGreaterThanOrEqual(FLOOR)

    // The three live signals, still drawn.
    const inside = await badge.evaluate((el) => ({
      dot: !!el.querySelector('[data-trust="tier-dot"]'),
      egress: !!el.querySelector('[data-trust="egress"]'),
      pill: Math.round(el.querySelector('.vtrust-pill')?.getBoundingClientRect().height ?? 0),
      described: document.getElementById(el.getAttribute('aria-describedby') || '')?.textContent?.trim() ?? '',
    }))
    expect(inside.dot, 'the tier dot is gone').toBe(true)
    expect(inside.egress, 'the egress glyph is gone').toBe(true)
    // The visible pill GREW rather than being padded out by an invisible hit
    // area. "Bigger without being quieter" is the whole instruction, and a 24px
    // pill inside a 44px transparent box would satisfy the floor while making
    // the trust surface no easier to read.
    expect(inside.pill, 'the visible pill did not grow').toBeGreaterThanOrEqual(28)
    // And the tier is stated in WORDS, not only in colour. It is on the
    // description rather than the name because the name is the stable
    // identifier three other suites refer to this control by.
    expect(inside.described, 'the tier is not stated in words').toMatch(/\w/)
    expect(inside.described.toLowerCase()).toContain('egress')

    await page.screenshot({ path: `${SHOTS}/phone-390-trustbadge.png` })
  })

  test('the status bar did not grow to pay for it', async ({ page }) => {
    // The controls fill the bar's existing 44px height rather than making the
    // bar taller — a phone gives up screen for chrome very reluctantly.
    await boot(page)
    const bar = await page.locator('.vmob-bar').boundingBox()
    if (!bar) throw new Error('no status bar')
    expect(bar.height).toBeLessThanOrEqual(56)
  })

  test('no horizontal overflow with every status control at full size', async ({ page }) => {
    // The bar carries wifi, battery, notifications, theme, the clock, the public
    // -apps warning AND the trust badge. Widening all of them to 44px is exactly
    // the change that pushes the row past 390px and shoves the warning badge —
    // the one element that must never be lost — off the right edge.
    await boot(page)
    const o = await page.evaluate(() => ({
      doc: document.documentElement.scrollWidth,
      bar: document.querySelector('.vmob-bar')?.scrollWidth ?? 0,
      inner: window.innerWidth,
    }))
    expect(o.doc).toBeLessThanOrEqual(o.inner + 1)
    expect(o.bar, `status bar scrollWidth ${o.bar} > ${o.inner}`).toBeLessThanOrEqual(o.inner + 1)
  })
})

for (const t of [{ w: 768, h: 1024 }, { w: 834, h: 1194 }]) {
  test.describe(`tablet ${t.w}×${t.h} (touch)`, () => {
    test.use({ viewport: { width: t.w, height: t.h }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 })

    test('nothing in the shell chrome is under 44px', async ({ page }) => {
      await boot(page)
      const small = await smallTargets(page, CHROME_ROOTS)
      await page.screenshot({ path: `${SHOTS}/tablet-${t.w}-chrome.png` })
      expect(small, `sub-${FLOOR}px targets: ${JSON.stringify(small, null, 2)}`).toEqual([])
    })
  })
}

test.describe('desktop 1280×800 (mouse) — the dense chrome is preserved', () => {
  test.use({ viewport: { width: 1280, height: 800 }, hasTouch: false, isMobile: false })

  test('the menu bar stays compact on a pointer', async ({ page }) => {
    // The 44px floor is a property of the INPUT, not of the app. Forcing it on a
    // mouse-driven desktop would inflate the menu bar for no one's benefit, so
    // the touch sizing is behind (pointer: coarse) and this proves it did not
    // leak.
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    await stubTelemetry(page)
    await installBackend(page)
    await page.goto('/')
    const badge = page.getByRole('button', { name: 'Sovereignty and privacy status' })
    await expect(badge).toBeVisible({ timeout: 25_000 })
    const box = await badge.boundingBox()
    if (!box) throw new Error('no badge')
    expect(box.height, 'the desktop trust badge grew').toBeLessThan(FLOOR)
    await page.screenshot({ path: `${SHOTS}/desktop-1280-menubar.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})
