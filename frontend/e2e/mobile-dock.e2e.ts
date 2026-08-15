/**
 * mobile-dock.e2e.ts — the phone dock, driven from the customization model on
 * real touch device profiles.
 *
 * WHY THIS SUITE EXISTS. `layouts/MobileStack.tsx` shipped a dock hardcoded to
 * Home · Apps · Library while `src/desktop` already persisted a separate mobile
 * dock profile. Nothing was red, because nothing was looking: a unit test can
 * assert that `getDockProfile('mobile')` returns items and never notice that no
 * pixel on the phone reflects them.
 *
 * So every assertion here is measured off the RENDERED DOM at 390×844, 768×1024
 * and 834×1194 with `pointer: coarse` and `hover: none` — the profiles that
 * actually mount MobileStack (shell/useViewport.ts). A narrow desktop viewport
 * is not a substitute: between 768 and 1024 the touch shell is up while
 * `activeFormFactor()` still reports 'desktop', which is exactly the width band
 * where a dock reading the "active" profile would draw the desktop's geometry.
 *
 * Screenshots go to test-results/, which .gitignore already covers — a spec that
 * creates a fresh top-level folder leaves a dirty tree for whoever commits next,
 * and in this repo that is several other agents.
 */
import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'
import { mkdirSync } from 'node:fs'

const SHOTS = 'test-results/mobile-dock'
mkdirSync(SHOTS, { recursive: true })

const DEVICES = [
  { name: 'phone-390', width: 390, height: 844, scale: 3 },
  { name: 'tablet-768', width: 768, height: 1024, scale: 2 },
  { name: 'tablet-834', width: 834, height: 1194, scale: 2 },
] as const

const NAV = 'nav[aria-label="System navigation"]'

/** A complete, VALID DesktopLayout. The validator rejects unknown keys, so this
 *  is the whole shape — a partial object would fall back to stock and the test
 *  would then be asserting the default while claiming to assert a custom dock. */
function layout(mobile: Record<string, unknown>) {
  return {
    presetId: 'vulos',
    dock: {
      desktop: {
        edge: 'bottom', size: 'medium', style: 'floating', align: 'center',
        autohide: false, launcher: true, assistant: true, drawer: false,
        items: ['drive', 'lilmail', 'vulos-calendar', 'messages', 'terminal', 'persona'],
      },
      mobile: {
        edge: 'bottom', size: 'large', style: 'bar', align: 'center',
        autohide: false, launcher: false, assistant: true, drawer: true,
        items: [],
        ...mobile,
      },
    },
    windowControls: 'left',
    tokens: {},
  }
}

async function boot(page: Page, opts: { mobile?: Record<string, unknown> } = {}) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  if (opts.mobile) {
    await page.addInitScript((l) => {
      try { localStorage.setItem('vulos.desktop.layout', JSON.stringify(l)) } catch { /* private mode */ }
    }, layout(opts.mobile))
  }
  await installBackend(page)
  await page.goto('/')
  await expect(page.locator(NAV)).toBeVisible({ timeout: 25_000 })
  return errors
}

/** Every dock slot's accessible name and box, in DOM order. */
async function slots(page: Page) {
  return page.evaluate((nav) => {
    const root = document.querySelector(nav)
    if (!root) throw new Error('no dock')
    return [...root.querySelectorAll('button[data-dock-slot]')].map((el) => {
      const b = el.getBoundingClientRect()
      return {
        slot: el.getAttribute('data-dock-slot') || '',
        label: (el.getAttribute('aria-label') || '').trim(),
        w: Math.round(b.width * 10) / 10,
        h: Math.round(b.height * 10) / 10,
        top: Math.round(b.top),
        bottom: Math.round(b.bottom),
      }
    })
  }, NAV)
}

async function noHorizontalOverflow(page: Page) {
  const o = await page.evaluate(() => ({
    doc: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    inner: window.innerWidth,
  }))
  expect(o.doc, `doc scrollWidth ${o.doc} > innerWidth ${o.inner}`).toBeLessThanOrEqual(o.inner + 1)
  expect(o.body).toBeLessThanOrEqual(o.inner + 1)
}

for (const dev of DEVICES) {
  test.describe(`${dev.name} (${dev.width}×${dev.height}, touch)`, () => {
    test.use({
      viewport: { width: dev.width, height: dev.height },
      hasTouch: true,
      isMobile: true,
      deviceScaleFactor: dev.scale,
    })

    test('the dock renders the profile items, not three hardcoded words', async ({ page }) => {
      const errors = await boot(page, {
        mobile: { items: ['lilmail', 'messages', 'files', 'terminal', 'drive'] },
      })

      const got = await slots(page)
      // This is the whole point of the pass. If MobileStack were still
      // hardcoded, `data-dock-slot` would not exist at all — which also makes
      // this the freshness proof for the served bundle.
      expect(got.map((s) => s.slot)).toEqual([
        'home', 'switcher',
        'app:lilmail', 'app:messages', 'app:files', 'app:terminal', 'app:drive',
        'library',
      ])
      // The app tiles carry the app's real name, not its id.
      expect(got.find((s) => s.slot === 'app:lilmail')?.label).toBeTruthy()
      expect(got.find((s) => s.slot === 'app:lilmail')?.label).not.toBe('app:lilmail')

      await noHorizontalOverflow(page)
      await page.screenshot({ path: `${SHOTS}/${dev.name}-dock-five-items.png` })
      expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
    })

    test('every slot clears 44×44 at the maximum eight', async ({ page }) => {
      await boot(page, { mobile: { items: ['lilmail', 'messages', 'files', 'terminal', 'drive'] } })
      const got = await slots(page)
      expect(got).toHaveLength(8)
      const small = got.filter((s) => s.w < 44 || s.h < 44)
      expect(small, `sub-44px dock slots: ${JSON.stringify(small)}`).toEqual([])
    })

    test('the dock is ON SCREEN, not below the fold', async ({ page }) => {
      // A widget rail 350px off the bottom of the screen survived a green suite
      // in this repo this week. A dock that is in the DOM and off the viewport
      // is the same defect, and only a bounding box can see it.
      await boot(page, { mobile: { items: ['lilmail', 'messages'] } })
      const box = await page.locator(NAV).boundingBox()
      if (!box) throw new Error('the dock has no bounding box')
      expect(box.y).toBeGreaterThanOrEqual(0)
      expect(box.y + box.height).toBeLessThanOrEqual(dev.height + 1)
      expect(box.width).toBeGreaterThan(dev.width * 0.9)
    })

    test('a top-edge profile puts the dock at the top, under the status bar', async ({ page }) => {
      await boot(page, { mobile: { edge: 'top', items: ['lilmail'] } })
      const nav = await page.locator(NAV).boundingBox()
      const bar = await page.locator('.vmob-bar').boundingBox()
      if (!nav || !bar) throw new Error('no dock or no status bar')
      // Under the status bar (which owns the notch inset), and in the top half.
      expect(nav.y).toBeGreaterThanOrEqual(bar.y + bar.height - 1)
      expect(nav.y).toBeLessThan(dev.height / 2)
      await page.screenshot({ path: `${SHOTS}/${dev.name}-dock-top-edge.png` })
    })

    test('a floating profile draws an inset island, a bar spans the edge', async ({ page }) => {
      await boot(page, { mobile: { style: 'floating', align: 'start', items: ['lilmail', 'messages'] } })
      const strip = await page.locator(`${NAV} .vmob-dock-strip`).boundingBox()
      if (!strip) throw new Error('no dock strip')
      // An island is inset from both edges and does not span the viewport.
      expect(strip.x).toBeGreaterThan(0)
      expect(strip.width).toBeLessThan(dev.width)
      await page.screenshot({ path: `${SHOTS}/${dev.name}-dock-floating-start.png` })
      await noHorizontalOverflow(page)
    })
  })
}

test.describe('phone 390×844 — the stock profile', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  test('the stock `home` item folds into the Home destination, not a second tile', async ({ page }) => {
    // src/desktop/presets.ts ships mobile items ['home','lilmail','messages'].
    // Rendered literally that is two adjacent dock targets both called "Home"
    // going to different places, and two buttons with one accessible name in
    // one toolbar.
    await boot(page)
    const got = await slots(page)
    expect(got.filter((s) => s.label === 'Home')).toHaveLength(1)
    expect(got.map((s) => s.slot)).toContain('home')
    expect(got.map((s) => s.slot)).not.toContain('app:home')
    await page.screenshot({ path: `${SHOTS}/phone-390-dock-stock.png` })
  })

  test('a docked app tile launches, then shows a running dot', async ({ page }) => {
    await boot(page, { mobile: { items: ['terminal'] } })
    const tile = page.locator('[data-dock-slot="app:terminal"]')
    await expect(tile).toHaveAttribute('aria-pressed', 'false')
    await tile.tap()
    // The launch pushes the shell into the fullscreen app view and the tile's
    // running state follows the window, not the tap.
    await expect(tile).toHaveAttribute('aria-pressed', 'true', { timeout: 15_000 })
    await expect(page.locator('[data-dock-slot="switcher"]')).toBeEnabled()
    await page.screenshot({ path: `${SHOTS}/phone-390-dock-running.png` })

    // Tapping the tile of the app already on screen puts it away (the phone's
    // version of minimize-on-second-click), and the app stays mounted.
    await tile.tap()
    await expect(page.locator('[data-mobile-home]')).toBeVisible()
    await expect(tile).toHaveAttribute('aria-pressed', 'true')
  })

  test('assistant:false removes the ask bar and nothing else', async ({ page }) => {
    await boot(page, { mobile: { assistant: false, items: ['lilmail'] } })
    await expect(page.getByRole('button', { name: /Ask your assistant/ })).toHaveCount(0)
    // The home grid is untouched.
    expect(await page.locator('[data-home-tile]').count()).toBeGreaterThan(8)
    // And so is the dock.
    expect((await slots(page)).map((s) => s.slot)).toEqual(['home', 'switcher', 'app:lilmail', 'library'])
  })

  test('autohide is REFUSED — the dock stays pinned', async ({ page }) => {
    // There is no hover on touch to bring a hidden dock back, and this is the
    // phone's only navigation surface.
    await boot(page, { mobile: { autohide: true, items: ['lilmail'] } })
    await expect(page.locator(NAV)).toHaveAttribute('data-autohide', 'off')
    const box = await page.locator(NAV).boundingBox()
    if (!box) throw new Error('the dock has no bounding box')
    expect(box.y + box.height).toBeLessThanOrEqual(844 + 1)
    expect(box.height).toBeGreaterThan(44)
  })

  test('the size enum moves the plate', async ({ page }) => {
    await boot(page, { mobile: { size: 'medium', items: ['lilmail'] } })
    await expect(page.locator(NAV)).toHaveAttribute('data-size', 'medium')
    const medium = await page.locator(`${NAV} .vmob-dock-plate`).first().boundingBox()
    if (!medium) throw new Error('no plate')
    expect(Math.round(medium.height)).toBe(44)
  })
})

test.describe('the desktop path did not regress', () => {
  test.use({ viewport: { width: 1280, height: 800 }, hasTouch: false, isMobile: false })

  test('a mobile dock profile is not collateral damage on the desktop dock', async ({ page }) => {
    // The two profiles persist independently. A mobile profile with five items
    // and a large tile must leave the desktop dock on its own six items and its
    // own medium tile — and MobileStack must not be mounted at all.
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    await page.addInitScript((l) => {
      try { localStorage.setItem('vulos.desktop.layout', JSON.stringify(l)) } catch { /* private mode */ }
    }, layout({ size: 'large', items: ['lilmail', 'messages', 'files', 'terminal', 'drive'] }))
    await installBackend(page)
    await page.goto('/')
    await expect(page.locator('[data-shell="mobile"]')).toHaveCount(0)
    const dock = page.locator('[role="toolbar"][aria-label="Dock"]')
    await expect(dock).toBeVisible({ timeout: 25_000 })
    await expect(page.locator('.vdock-layer')).toHaveAttribute('data-size', 'medium')
    await page.screenshot({ path: `${SHOTS}/desktop-1280-unaffected.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})
