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

/**
 * Prove the scan sees the three kinds of text that are NOT textContent.
 *
 * Placeholders, ::before/::after content and input values are all text a reader
 * looks at and all invisible to a textContent walk. The placeholder case was a
 * real defect that sat at 2.42:1 in plain sight while every gate reported the
 * shell clean, precisely because the scan could not reach it.
 *
 * Generated content and input values measure clean in both corpora today, so
 * nothing currently exercises those branches — which is the same state the
 * placeholder was in. Injecting each one is what distinguishes "no failures"
 * from "cannot see failures".
 *
 * A source mutation was the first attempt and it fired nothing: the class went
 * onto a JSX element that already had a className, so it never applied. An
 * in-page injection is unambiguous.
 */
test('the scan reports placeholder, generated and value text it cannot get from textContent', async ({ page }) => {
  test.setTimeout(120_000)
  await page.setViewportSize(PHONE)
  await installBackend(page)
  await page.goto('/')
  await expect(page.locator('nav[aria-label="System navigation"]')).toBeVisible({ timeout: 20_000 })
  await page.waitForTimeout(1200)

  const clean = await belowAA(page)
  expect(clean, 'the shell must start clean for this test to mean anything').toEqual([])

  await page.evaluate(() => {
    const host = document.createElement('div')
    host.id = 'contrast-probe'
    // Near-identical to its own background: about 1.1:1 whichever way.
    host.setAttribute('style', 'background:#111; color:#151515; padding:8px')

    const style = document.createElement('style')
    style.textContent = '#contrast-probe .gen::after { content: "GENERATED"; color: #171717; font-size: 12px }'
    document.head.appendChild(style)

    const gen = document.createElement('span')
    gen.className = 'gen'
    host.appendChild(gen)

    const withValue = document.createElement('input')
    withValue.value = 'VALUE PROBE'
    withValue.setAttribute('style', 'color:#151515; background:#111; font-size:12px')
    host.appendChild(withValue)

    const withPlaceholder = document.createElement('input')
    withPlaceholder.placeholder = 'PLACEHOLDER PROBE'
    withPlaceholder.setAttribute('style', 'color:#151515; background:#111; font-size:12px')
    document.head.insertAdjacentHTML(
      'beforeend',
      '<style>#contrast-probe input::placeholder { color:#151515; opacity:1 }</style>',
    )
    host.appendChild(withPlaceholder)

    document.body.appendChild(host)
  })
  await page.waitForTimeout(300)

  const found = (await belowAA(page)).join('\n')
  expect(found, 'a ::after label at ~1.1:1 was not reported').toContain('GENERATED')
  expect(found, 'an input VALUE at ~1.1:1 was not reported').toContain('VALUE PROBE')
  expect(found, 'a PLACEHOLDER at ~1.1:1 was not reported').toContain('PLACEHOLDER PROBE')

  await page.evaluate(() => document.getElementById('contrast-probe')?.remove())
  expect(
    await belowAA(page),
    'the scan kept reporting after the probe was removed',
  ).toEqual([])
})
