// windows-open-geometry.e2e.ts — a freshly opened window must be FULLY on
// screen, at every viewport width the desktop canvas is mounted at.
//
// WHY THIS EXISTS
//
// ShellProvider's OPEN_WINDOW used to clamp neither size nor position. It
// placed window N at x = 60 + (N % 6) * 32 and sized it 720x500 flat, so the
// first window's right edge sat at 780px — 12px past the right edge of a 768px
// viewport, and 172px past it by the sixth window in the cascade. Nothing in
// the unit suite could see it: the reducer is a pure function and 780 > 768 is
// only a defect once you know how wide the screen is.
//
// The desktop canvas is mounted from 768px up (below that, and on a
// coarse-pointer tablet up to 1024px, shell/viewportRule.ts hands over to
// MobileStack, which renders windows full-bleed and has no floating geometry
// at all). So 768 is not a hypothetical width — it is the NARROWEST width at
// which a floating window is ever drawn, and it was the width that failed
// worst.
//
// Assertions here are measured pixel rects from the real browser, not class
// names: `boundingBox()` of every `[data-window-id]`, compared against the
// viewport. That is the only check that would have caught the original bug.

import { test, expect, type Page, type Browser } from '@playwright/test'
import { installBackend } from './mock-backend.js'

// Widths the desktop canvas can actually be mounted at. 768 is the floor
// (MOBILE_BREAKPOINT); the rest are the common tablet-portrait / small-laptop /
// laptop extents. Heights are the real device/window heights that go with them,
// because the vertical clamp is against MENU_BAR_H + DOCK_H, not just width.
const CANVAS_VIEWPORTS = [
  { name: '768x1024', width: 768, height: 1024 },
  { name: '834x1194', width: 834, height: 1194 },
  { name: '1024x768', width: 1024, height: 768 },
  { name: '1440x900', width: 1440, height: 900 },
]

// Six, because the open cascade wraps at six (index % 6) — opening fewer would
// never reach the largest offset, which is where the overflow was worst.
const APPS = ['Calculator', 'Clock', 'Weather', 'Text Editor', 'System Info', 'Voice Recorder']

async function boot(page: Page) {
  // Reduced motion, deliberately: shell/Window.tsx's open choreography scales
  // the window from a transform for ~1 frame, so a boundingBox() taken while
  // it is in flight reports a SMALLER box than the geometry the shell actually
  // chose. Measured mid-animation, a 720px window at x=223 read as 714px at
  // x=223 — 3px of overflow hidden by the animation. Under `reduce` the phase
  // starts at 'open' with no transform, so the rect is the real layout box.
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await installBackend(page)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
}

// Launch through the ⌘K palette — the same real launchApp path the Launchpad
// uses, but click-stable in a headless run.
async function launch(page: Page, appName: string) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(appName)
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(input).toBeHidden()
}

interface WinRect { id: string; x: number; y: number; w: number; h: number }

async function windowRects(page: Page): Promise<WinRect[]> {
  return page.$$eval('[data-window-id]', els => els.map(el => {
    const r = el.getBoundingClientRect()
    return { id: el.getAttribute('data-window-id') || '?', x: r.x, y: r.y, w: r.width, h: r.height }
  }))
}

/** How far a rect pokes outside the viewport on each side, in px. */
function overflow(r: WinRect, vw: number, vh: number) {
  return {
    left: Math.max(0, -r.x),
    top: Math.max(0, -r.y),
    right: Math.max(0, r.x + r.w - vw),
    bottom: Math.max(0, r.y + r.h - vh),
  }
}

for (const vp of CANVAS_VIEWPORTS) {
  test(`every freshly opened window fits inside a ${vp.name} viewport`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    await boot(page)

    // The desktop canvas must actually be up — otherwise this test would pass
    // by measuring zero floating windows (MobileStack draws none), which is
    // precisely the hollow-gate shape this repo keeps finding.
    for (const app of APPS) await launch(page, app)
    const rects = await windowRects(page)
    expect(rects.length, 'desktop canvas should have drawn one floating window per launch').toBe(APPS.length)

    const offenders = rects
      .map(r => ({ r, o: overflow(r, vp.width, vp.height) }))
      .filter(({ o }) => o.left > 0 || o.top > 0 || o.right > 0 || o.bottom > 0)
      .map(({ r, o }) => `win ${r.id} at (${Math.round(r.x)},${Math.round(r.y)}) ${Math.round(r.w)}x${Math.round(r.h)} overflows L${Math.round(o.left)} T${Math.round(o.top)} R${Math.round(o.right)} B${Math.round(o.bottom)}`)

    await page.screenshot({ path: `test-results/windows-open-${vp.name}.png`, fullPage: false })
    console.log(`[${vp.name}] ` + rects.map(r => `#${r.id} (${Math.round(r.x)},${Math.round(r.y)}) ${Math.round(r.w)}x${Math.round(r.h)}`).join('  '))
    expect(offenders, `windows opened outside the ${vp.name} viewport`).toEqual([])
  })
}

// The dock is always present and floats over the bottom DOCK_H px (see
// windowTiling.ts). A window that opens UNDER it is on-screen by the rect test
// above and still unusable, so the vertical fit is asserted against the usable
// band, not the raw viewport.
const MENU_BAR_H = 32
const DOCK_H = 68

test('a freshly opened window clears the menu bar and the dock at 1024x768', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 768 })
  await boot(page)
  for (const app of APPS) await launch(page, app)
  const rects = await windowRects(page)
  expect(rects.length).toBe(APPS.length)
  for (const r of rects) {
    expect(r.y, `win ${r.id} top`).toBeGreaterThanOrEqual(MENU_BAR_H)
    expect(r.y + r.h, `win ${r.id} bottom`).toBeLessThanOrEqual(768 - DOCK_H)
  }
})

// The correction to the original report: an iPad in PORTRAIT does not run the
// desktop canvas at all — MOBILE-07 (shell/viewportRule.ts) hands a
// coarse-pointer viewport up to 1024px to MobileStack, which draws no floating
// windows. Pinned here so a future change to that rule cannot quietly put
// unclamped floating windows back on a tablet without this failing.
test('a coarse-pointer 768x1024 tablet gets MobileStack, not floating windows', async ({ browser }: { browser: Browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 768, height: 1024 }, hasTouch: true, isMobile: true })
  const page = await ctx.newPage()
  await installBackend(page)
  await page.goto('/')
  await page.waitForTimeout(2000)
  expect(await page.evaluate(() => window.matchMedia('(pointer: coarse) and (hover: none)').matches)).toBe(true)
  expect(await page.$$eval('[data-window-id]', els => els.length)).toBe(0)
  await ctx.close()
})
