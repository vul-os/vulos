// mobile-shell.e2e.js — real-chromium proof that the OS shell ADAPTS to a phone
// (WAVE-30). At narrow viewports the desktop metaphor must not shrink into tiny
// draggable windows; instead the shell renders MobileStack: a safe-area bottom
// dock, fullscreen apps, and a phone-style app switcher.
//
// The whole point of the mobile work is "usable on a phone, nothing overflows
// sideways", so every test here asserts NO horizontal overflow and fails on any
// uncaught page error (a mobile-only layout is exactly the kind of change that
// can white-screen the shell).
//
// The backend is mocked at the browser network layer via mock-backend.js — no Go
// server, no DB. Layout selection is viewport-driven (useViewport < 768px →
// mobile), so simply narrowing the viewport switches the shell to MobileStack.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

// iPhone-class viewport (390×844 ≈ iPhone 13/14).
test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

async function bootMobile(page: Page, overrides: Record<string, unknown> = {}) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await installBackend(page, overrides)
  await page.goto('/')
  // The mobile shell's single navigation surface — proves MobileStack mounted
  // (the desktop DesktopCanvas has no such nav).
  await expect(page.locator('nav[aria-label="System navigation"]')).toBeVisible({ timeout: 15_000 })
  return errors
}

// No element may push the page wider than the viewport.
async function assertNoHorizontalOverflow(page: Page) {
  const overflow = await page.evaluate(() => ({
    docScroll: document.documentElement.scrollWidth,
    docClient: document.documentElement.clientWidth,
    bodyScroll: document.body.scrollWidth,
    inner: window.innerWidth,
  }))
  expect(overflow.docScroll, `doc scrollWidth ${overflow.docScroll} > innerWidth ${overflow.inner}`)
    .toBeLessThanOrEqual(overflow.inner + 1)
  expect(overflow.bodyScroll).toBeLessThanOrEqual(overflow.inner + 1)
}

// Launch a builtin by name via the ⌘K palette (the same lane the dock's
// launcher uses); works identically on mobile.
async function launch(page: Page, name: string) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(name)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
}

test('mobile shell renders the adaptive dock, no horizontal overflow', async ({ page }) => {
  const errors = await bootMobile(page)

  const nav = page.locator('nav[aria-label="System navigation"]')
  // The three thumb-reachable destinations.
  await expect(nav.getByRole('button', { name: 'Home' })).toBeVisible()
  await expect(nav.getByRole('button', { name: 'Apps' })).toBeVisible()
  await expect(nav.getByRole('button', { name: 'Library' })).toBeVisible()

  // Every dock target is a full touch target (≥44px tall).
  const homeBox = await nav.getByRole('button', { name: 'Home' }).boundingBox()
  if (!homeBox) throw new Error('Home dock button has no bounding box')
  expect(homeBox.height).toBeGreaterThanOrEqual(44)

  await assertNoHorizontalOverflow(page)
  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('launching an app makes it fullscreen with a back-to-home affordance', async ({ page }) => {
  const errors = await bootMobile(page, { 'GET /api/pim/calendar/events': json({ events: [] }) })

  await launch(page, 'Calendar')

  // The app renders fullscreen (its own root fills the content area).
  const app = page.locator('[data-calendar-app]')
  await expect(app).toBeVisible()
  const box = await app.boundingBox()
  if (!box) throw new Error('Calendar app root has no bounding box')
  expect(box.width).toBeGreaterThan(320) // ~full 390px viewport, not a tiny window

  // The status bar swaps to the app identity with a back-to-home control.
  await expect(page.getByRole('button', { name: 'Back to home' })).toBeVisible()

  await assertNoHorizontalOverflow(page)

  // Back returns to the Home (assistant) surface; the app stays mounted/running.
  await page.getByRole('button', { name: 'Back to home' }).click()
  await expect(page.getByRole('button', { name: 'Back to home' })).toBeHidden()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the app switcher lists running apps and can close them', async ({ page }) => {
  const errors = await bootMobile(page, { 'GET /api/pim/calendar/events': json({ events: [] }) })

  await launch(page, 'Calendar')
  await expect(page.locator('[data-calendar-app]')).toBeVisible()

  // Open the switcher from the dock.
  await page.locator('nav[aria-label="System navigation"]').getByRole('button', { name: 'Apps' }).click()
  await expect(page.getByText('Running apps')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Close Calendar' })).toBeVisible()

  await assertNoHorizontalOverflow(page)

  // Closing the only running app drops back to Home (no apps left).
  await page.getByRole('button', { name: 'Close Calendar' }).click()
  await expect(page.getByText('Running apps')).toBeHidden()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('a two-pane builtin (Contacts) collapses to one pane on a phone', async ({ page }) => {
  const errors = await bootMobile(page, {
    'GET /api/pim/contacts/cards': json({ contacts: [
      { uid: 'c1', name: 'Ada Lovelace', org: 'Analytical', title: '', note: '', emails: ['ada@vulos.org'], phones: [] },
    ] }),
  })

  await launch(page, 'Contacts')
  const app = page.locator('[data-contacts-app]')
  await expect(app).toBeVisible()

  // The list shows; picking a contact swaps to the full-width detail with a back
  // control (single-pane), rather than a cramped side-by-side split.
  await app.getByText('Ada Lovelace').first().click()
  await expect(app.getByRole('button', { name: /Contacts/ })).toBeVisible()
  await expect(app.locator('[data-contact-detail]')).toBeVisible()

  await assertNoHorizontalOverflow(page)
  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})
