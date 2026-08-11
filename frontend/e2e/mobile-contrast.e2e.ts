import { test, expect } from '@playwright/test'
import { installBackend } from './mock-backend.js'
import { belowAA, textNodeCount } from './contrast-scan'

/**
 * The MOBILE shell meets WCAG AA, in both themes.
 *
 * Layout selection is viewport-driven (useViewport < 768px → MobileStack), so a
 * narrow viewport is a genuinely different tree — a bottom nav, a portal sheet,
 * no dock or menu bar. The desktop contrast gates run at 1440 and never see any
 * of it, and it had its own failures: the ACTIVE nav label used the raw
 * user-chosen accent at 3.28:1 instead of the derived --accent-text, and the
 * assistant's empty state sat at 2.42:1.
 *
 * The boot marker differs too. bootShell waits for the desktop's Applications
 * control, which does not exist here; MobileStack's own `nav[aria-label="System
 * navigation"]` is what proves it mounted.
 */

const PHONE = { width: 390, height: 844 }

for (const theme of ['dark', 'light'] as const) {
  test(`the mobile shell meets WCAG AA on the ${theme} theme`, async ({ page }) => {
    test.setTimeout(120_000)
    await page.addInitScript((t) => {
      try { localStorage.setItem('vulos-theme', t as string) } catch { /* private mode */ }
    }, theme)
    await page.setViewportSize(PHONE)
    await installBackend(page)
    await page.goto('/')
    await expect(page.locator('nav[aria-label="System navigation"]')).toBeVisible({ timeout: 20_000 })
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t as string), theme)
    await page.waitForTimeout(1800)

    const measured = await textNodeCount(page)
    expect(measured, 'the mobile shell rendered almost no text').toBeGreaterThan(5)

    expect(
      await belowAA(page),
      `mobile shell text below WCAG AA on ${theme}, measured on composited pixels`,
    ).toEqual([])

    // The phone shell must never scroll sideways.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    )
    expect(overflow, 'the mobile shell scrolls sideways').toBeLessThanOrEqual(2)
  })
}
