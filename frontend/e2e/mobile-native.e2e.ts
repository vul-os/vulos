// mobile-native.e2e.ts — the mobile shell has to behave like a phone OS, at real
// device profiles, with touch, in real Chromium against the real production
// build. Every assertion here corresponds to a defect that was MEASURED on the
// shipping build, not to a hypothetical.
//
// The existing mobile-shell.e2e.ts covers the phone at 390×844. This file adds
// the three things that spec could not see:
//   - TABLETS. 768×1024 and 834×1194 were rendering the DESKTOP canvas, with
//     12×12 px window controls, on a touch screen. A narrow desktop viewport
//     never reveals that, because the bug needs `pointer: coarse`.
//   - The switcher's DOUBLE MOUNT, which is invisible to any test that only
//     asks "is the card visible?".
//   - The desktop control, so none of this is bought by regressing the desktop.
//
// Backend is mocked at the browser network layer (mock-backend.js). No Go
// server, no DB.

import { test, expect, type Page, type BrowserContext } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

const CALENDAR_EMPTY = { 'GET /api/pim/calendar/events': json({ events: [] }) }

const NAV = 'nav[aria-label="System navigation"]'

async function boot(page: Page, overrides: Record<string, unknown> = {}) {
  const errors: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  await installBackend(page, overrides)
  await page.goto('/')
  return errors
}

async function bootMobile(page: Page, overrides: Record<string, unknown> = {}) {
  const errors = await boot(page, overrides)
  await expect(page.locator(NAV)).toBeVisible({ timeout: 20_000 })
  return errors
}

// Launch a builtin through the ⌘K palette — the same lane every launcher uses.
async function launch(page: Page, name: string) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 15_000 })
  await input.fill(name)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
}

// ───────────────────────────────────────────────────────────────────────────
// MOBILE-07 — a touch tablet is not a small desktop
//
// Before this rule, both of these profiles mounted DesktopCanvas: draggable
// windows, and `.vwin-light` close/minimise/maximise buttons measured at
// 12×12 px. The layout is chosen by shell/useViewport.ts, which now also
// consults (pointer: coarse) and (hover: none).
// ───────────────────────────────────────────────────────────────────────────

for (const tablet of [
  { name: 'tablet 768×1024', width: 768, height: 1024 },
  { name: 'tablet 834×1194', width: 834, height: 1194 },
]) {
  test.describe(`${tablet.name} (touch)`, () => {
    test.use({
      viewport: { width: tablet.width, height: tablet.height },
      hasTouch: true,
      isMobile: true,
      deviceScaleFactor: 2,
    })

    test('renders the TOUCH shell, not the desktop canvas', async ({ page }) => {
      const errors = await bootMobile(page, CALENDAR_EMPTY)

      // The definitive marker: MobileStack stamps data-shell="mobile".
      await expect(page.locator('[data-shell="mobile"]')).toHaveCount(1)
      // And the desktop canvas's own chrome is absent.
      await expect(page.locator('.vwin-light')).toHaveCount(0)

      expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
    })

    test('no interactive control inside the shell chrome is under 44px', async ({ page }) => {
      await bootMobile(page, CALENDAR_EMPTY)

      // Scoped to the surfaces this workstream owns — the home grid, the dock,
      // and the switcher. Widgets owned elsewhere (SystemPulse, TrustBadge) are
      // reported separately rather than silently folded into this floor, which
      // would make the number meaningless.
      const small = await page.evaluate(() => {
        const roots = ['[data-mobile-home]', 'nav[aria-label="System navigation"]', '[data-mobile-switcher]']
        const bad: { w: number; h: number; label: string }[] = []
        for (const root of roots) {
          for (const el of document.querySelectorAll(`${root} button, ${root} a[href]`)) {
            const b = el.getBoundingClientRect()
            if (b.width === 0 || b.height === 0) continue
            if (b.width < 44 || b.height < 44) {
              bad.push({
                w: Math.round(b.width), h: Math.round(b.height),
                label: (el.getAttribute('aria-label') || el.textContent || '?').trim().slice(0, 40),
              })
            }
          }
        }
        return bad
      })
      expect(small, `sub-44px targets: ${JSON.stringify(small)}`).toEqual([])
    })

    test('the app grid opens two-up wider than a phone', async ({ page }) => {
      await bootMobile(page, CALENDAR_EMPTY)
      const tiles = page.locator('[data-home-tile]')
      expect(await tiles.count()).toBeGreaterThan(8)
      // 6 columns at sm: — the first row's tiles must share a top edge, and the
      // 5th must still be on it (a 4-column phone grid would have wrapped).
      const first = await tiles.nth(0).boundingBox()
      const fifth = await tiles.nth(4).boundingBox()
      if (!first || !fifth) throw new Error('home tiles have no bounding box')
      expect(Math.abs(first.y - fifth.y)).toBeLessThan(2)
    })
  })
}

// ───────────────────────────────────────────────────────────────────────────
// Phone 390×844
// ───────────────────────────────────────────────────────────────────────────

test.describe('phone 390×844 (touch)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  // MOBILE-10 — a phone home screen with apps on it.
  test('home shows a real app grid, every tile a full touch target', async ({ page }) => {
    const errors = await bootMobile(page, CALENDAR_EMPTY)

    await expect(page.locator('[data-mobile-home="apps"]')).toBeVisible()
    const tiles = page.locator('[data-home-tile]')
    // The old home had ZERO apps on it. A floor well above zero is the point.
    expect(await tiles.count()).toBeGreaterThan(8)

    // 4 columns at phone width: tile 1 and tile 5 must be on different rows.
    const first = await tiles.nth(0).boundingBox()
    const fifth = await tiles.nth(4).boundingBox()
    if (!first || !fifth) throw new Error('home tiles have no bounding box')
    expect(fifth.y).toBeGreaterThan(first.y + 10)

    // Every tile clears the 44px floor in BOTH axes.
    const undersized = await page.evaluate(() =>
      [...document.querySelectorAll('[data-home-tile]')]
        .map(el => el.getBoundingClientRect())
        .filter(b => b.width < 44 || b.height < 44)
        .map(b => `${Math.round(b.width)}x${Math.round(b.height)}`)
    )
    expect(undersized, `undersized home tiles: ${undersized.join(', ')}`).toEqual([])

    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('tapping a home tile launches the app fullscreen', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    await page.locator('[data-home-tile="vulos-calendar"]').tap()
    const app = page.locator('[data-calendar-app]')
    await expect(app).toBeVisible({ timeout: 15_000 })
    const box = await app.boundingBox()
    if (!box) throw new Error('calendar has no bounding box')
    expect(box.width).toBeGreaterThan(320)
  })

  test('the ask bar is a button, not an input — Home does not summon the keyboard', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    const ask = page.getByRole('button', { name: 'Ask your assistant' })
    await expect(ask).toBeVisible()
    // No focused text field on arrival: an <input> here would open the keyboard
    // on every visit to Home and push the grid off screen.
    const tag = await page.evaluate(() => document.activeElement?.tagName ?? '')
    expect(['INPUT', 'TEXTAREA']).not.toContain(tag)
    // Tapping it hands the surface to the assistant, and there is a way back.
    await ask.tap()
    await expect(page.locator('[data-mobile-home="assistant"]')).toBeVisible()
    await page.getByRole('button', { name: 'Back to apps' }).tap()
    await expect(page.locator('[data-mobile-home="apps"]')).toBeVisible()
  })

  // MOBILE-09 — THE regression guard for this workstream.
  test('opening the switcher does NOT mount a second copy of a running app', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    await launch(page, 'Calendar')
    await expect(page.locator('[data-calendar-app]')).toBeVisible({ timeout: 15_000 })
    expect(await page.locator('[data-calendar-app]').count()).toBe(1)

    await page.locator(NAV).getByRole('button', { name: 'Apps' }).tap()
    await expect(page.locator('[data-mobile-switcher]')).toBeVisible()
    await expect(page.locator('[data-switcher-card="vulos-calendar"]')).toBeVisible()

    // The measured defect: this used to be 2. A live second instance of every
    // running app, spawned by the surface you open BECAUSE memory is tight.
    expect(
      await page.locator('[data-calendar-app]').count(),
      'the switcher mounted a second live copy of Calendar',
    ).toBe(1)
  })

  test('a switcher card closes by dragging it upward', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    await launch(page, 'Calendar')
    await expect(page.locator('[data-calendar-app]')).toBeVisible({ timeout: 15_000 })
    await page.locator(NAV).getByRole('button', { name: 'Apps' }).tap()

    const card = page.locator('[data-switcher-card="vulos-calendar"]')
    await expect(card).toBeVisible()
    const box = await card.boundingBox()
    if (!box) throw new Error('switcher card has no bounding box')

    // Pointer events, so the same code path a finger drives is driven here.
    const cx = box.x + box.width / 2
    const cy = box.y + box.height / 2
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx, cy - 220, { steps: 12 })
    await page.mouse.up()

    await expect(card).toHaveCount(0)
    await expect(page.locator('[data-calendar-app]')).toHaveCount(0)
  })

  test('a short drag springs back instead of closing', async ({ page }) => {
    // The dismissal threshold has to be a real threshold. Without this, a
    // gesture that fires on any movement at all would pass the test above while
    // making the deck impossible to scroll.
    await bootMobile(page, CALENDAR_EMPTY)
    await launch(page, 'Calendar')
    await expect(page.locator('[data-calendar-app]')).toBeVisible({ timeout: 15_000 })
    await page.locator(NAV).getByRole('button', { name: 'Apps' }).tap()

    const card = page.locator('[data-switcher-card="vulos-calendar"]')
    const box = await card.boundingBox()
    if (!box) throw new Error('switcher card has no bounding box')
    const cx = box.x + box.width / 2
    const cy = box.y + box.height / 2
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx, cy - 18, { steps: 4 })
    await page.mouse.up()

    await expect(card).toBeVisible()
    expect(await page.locator('[data-calendar-app]').count()).toBe(1)
  })

  test('the explicit close button still closes — the gesture is not the only path', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    await launch(page, 'Calendar')
    await expect(page.locator('[data-calendar-app]')).toBeVisible({ timeout: 15_000 })
    await page.locator(NAV).getByRole('button', { name: 'Apps' }).tap()
    await page.getByRole('button', { name: /^Close Calendar$/ }).tap()
    await expect(page.locator('[data-calendar-app]')).toHaveCount(0)
  })

  // MOBILE-08 — pull-to-refresh must not reload the OS.
  test('the root scroller contains overscroll, so pull-to-refresh cannot reload the shell', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    const behavior = await page.evaluate(() => ({
      html: getComputedStyle(document.documentElement).overscrollBehaviorY,
      body: getComputedStyle(document.body).overscrollBehaviorY,
      stamped: document.documentElement.classList.contains('vmob-active'),
    }))
    // Measured 'auto' on both before the fix.
    expect(behavior.html).toBe('contain')
    expect(behavior.body).toBe('contain')
    expect(behavior.stamped).toBe(true)
  })

  // Safe areas. env(safe-area-inset-*) resolves to 0 in headless Chromium, so
  // asserting the rendered padding directly would assert nothing. Instead the
  // token is driven to a known value and the chrome is required to MOVE — which
  // is the actual contract (`.safe-pb` reads var(--safe-bottom)).
  test('the dock and status bar pad themselves out of the safe areas', async ({ page }) => {
    await bootMobile(page, CALENDAR_EMPTY)
    const before = await page.evaluate(() => ({
      dock: getComputedStyle(document.querySelector('nav[aria-label="System navigation"]')!).paddingBottom,
      bar: getComputedStyle(document.querySelector('.vmob-bar')!).paddingTop,
    }))
    const after = await page.evaluate(() => {
      const root = document.documentElement
      root.style.setProperty('--safe-bottom', '34px')
      root.style.setProperty('--safe-top', '59px')
      return {
        dock: getComputedStyle(document.querySelector('nav[aria-label="System navigation"]')!).paddingBottom,
        bar: getComputedStyle(document.querySelector('.vmob-bar')!).paddingTop,
      }
    })
    expect(before.dock).toBe('0px')
    expect(before.bar).toBe('0px')
    // A home indicator would otherwise sit on top of the dock's targets, and a
    // notch on top of the status bar.
    expect(after.dock).toBe('34px')
    expect(after.bar).toBe('59px')
  })

  // MOBILE-11 — manifest launcher shortcuts have to actually open something.
  test('?open=<appId> launches that app, and does not survive a reload', async ({ page }) => {
    await installBackend(page, CALENDAR_EMPTY)
    await page.goto('/?open=vulos-calendar')
    await expect(page.locator('[data-calendar-app]')).toBeVisible({ timeout: 20_000 })
    // Consumed exactly once: the parameter is stripped, so a refresh (or the SW
    // replaying the start URL) does not relaunch on top of what the user is
    // doing.
    expect(new URL(page.url()).searchParams.get('open')).toBeNull()
  })

  test('?open= with an id that is not in the registry opens nothing', async ({ page }) => {
    const errors = await boot(page, CALENDAR_EMPTY)
    await page.goto('/?open=../../etc/passwd')
    await expect(page.locator(NAV)).toBeVisible({ timeout: 20_000 })
    await expect(page.locator('[data-mobile-home="apps"]')).toBeVisible()
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})

// ───────────────────────────────────────────────────────────────────────────
// Desktop control — none of the above may be bought by regressing the desktop.
// ───────────────────────────────────────────────────────────────────────────

test.describe('desktop 1440×900 (mouse) — control', () => {
  test.use({ viewport: { width: 1440, height: 900 }, hasTouch: false, isMobile: false })

  test('still renders the desktop canvas, not the mobile stack', async ({ page }) => {
    const errors = await boot(page, CALENDAR_EMPTY)
    await expect(page.locator('[data-shell="mobile"]')).toHaveCount(0, { timeout: 20_000 })
    await expect(page.locator(NAV)).toHaveCount(0)
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('the desktop root scroller is left alone — no overscroll containment', async ({ page }) => {
    await boot(page, CALENDAR_EMPTY)
    await expect(page.locator('[data-shell="mobile"]')).toHaveCount(0, { timeout: 20_000 })
    const behavior = await page.evaluate(() => ({
      html: getComputedStyle(document.documentElement).overscrollBehaviorY,
      stamped: document.documentElement.classList.contains('vmob-active'),
    }))
    // The mobile guard is scoped to the mobile shell. If this ever reads
    // 'contain', the class is leaking onto the desktop.
    expect(behavior.stamped).toBe(false)
    expect(behavior.html).toBe('auto')
  })

  test('a desktop window still opens with its draggable chrome', async ({ page, context }: { page: Page; context: BrowserContext }) => {
    void context
    await boot(page, CALENDAR_EMPTY)
    await expect(page.locator('[data-shell="mobile"]')).toHaveCount(0, { timeout: 20_000 })
    await launch(page, 'Calendar')
    await expect(page.locator('[data-calendar-app]')).toBeVisible({ timeout: 15_000 })
    // The desktop metaphor is intact: window lights exist on the desktop path.
    expect(await page.locator('.vwin-light').count()).toBeGreaterThan(0)
  })
})
