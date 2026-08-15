// widgets-contrast.e2e.ts — every string the widget rail renders, measured on
// COMPOSITED PIXELS, in BOTH themes.
//
// Measured rather than audited, because the failure is never a value in a file —
// it is a PAIR, and `opacity` is half of it. The desktop watermark that started
// this practice read `color: var(--accent)` in the source and rendered at 2.80:1
// because of an `opacity: 0.85` beside it; no stylesheet audit can see that. The
// widget rail is exactly the surface where this bites: every tile is a
// translucent card over a wallpaper, so the effective background of a label is
// something no token names.
//
// BOTH themes, because a gate that resolves the theme on its own booted light
// here once and left the dark palette — which most of this UI was designed
// against — completely unchecked.
//
// The rail's chrome (edit mode, the gallery, the settings panel) is scanned too:
// those surfaces only exist after a click, so a scan of the resting desktop
// would report the rail clean while half of its text had never been measured.

import { test, expect, type Page } from '@playwright/test'
import { bootShell, belowAA, textNodeCount } from './contrast-scan'

/** Put a window on screen so the desktop rail is the backdrop, not Home. */
async function openAWindow(page: Page, appName = 'Calculator') {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(appName)
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('button', { name: 'Close window' }).first()).toBeVisible()
  // WAIT FOR THE OPEN ANIMATION TO SETTLE before anything is measured.
  //
  // Not politeness — correctness. The scan composites an element's effective
  // background down the ancestor chain, and mid-animation the window's own
  // `opacity` is still climbing, so the composite falls back toward the white
  // base and the title bar's --text-secondary measured 2.11:1 against a
  // background it will never actually have. That is a BAD MEASUREMENT reporting
  // a defect that does not exist, which is worse than no measurement: it costs
  // exactly as much to chase as a real one.
  await page.waitForFunction(() => {
    const w = document.querySelector('.vwin') as HTMLElement | null
    if (!w) return false
    return parseFloat(getComputedStyle(w).opacity || '1') > 0.99
  }, undefined, { timeout: 10_000 })
  await page.waitForTimeout(400)
}

/** How much of the rail's own text was measured. A rail that failed to render must not pass. */
async function railTextCount(page: Page): Promise<number> {
  return page.evaluate(() => {
    const root = document.querySelector('[data-desktop-widgets]')
    if (!root) return 0
    return [...root.querySelectorAll('span,div,button,label,p')]
      .filter((e) => (e.textContent || '').trim() && !e.children.length).length
  })
}

for (const theme of ['dark', 'light'] as const) {
  test(`the widget rail has no sub-AA text in the ${theme} theme`, async ({ page }) => {
    await page.addInitScript(() => {
      try { localStorage.removeItem('vulos.widgets.layout.v1') } catch { /* private mode */ }
    })
    await bootShell(page, theme)
    await openAWindow(page)
    await expect(page.locator('[data-widget-rail]')).toBeVisible()

    // COVERAGE FLOOR. Without it this test passes on a rail that rendered
    // nothing at all — the single most common way a contrast gate reports clean
    // while checking zero strings.
    //
    // The numbers are MEASURED, not guessed: the shipped default rail renders 23
    // text nodes of its own inside a 40-node desktop. The floors sit just under
    // those so a widget silently failing to render fails this test, while normal
    // copy edits do not. A floor picked out of the air is either unfailable or
    // permanently annoying; the first version of this line used 40 and failed on
    // the true value of exactly 40.
    const railText = await railTextCount(page)
    expect(railText, 'the rail rendered almost no text — the scan would be vacuous').toBeGreaterThan(18)
    expect(await textNodeCount(page), 'the desktop rendered almost no text').toBeGreaterThan(32)

    const failures = await belowAA(page)
    expect(failures, `sub-AA text in ${theme}:\n${failures.join('\n')}`).toEqual([])
  })

  test(`the rail's edit chrome has no sub-AA text in the ${theme} theme`, async ({ page }) => {
    await page.addInitScript(() => {
      try { localStorage.removeItem('vulos.widgets.layout.v1') } catch { /* private mode */ }
    })
    await bootShell(page, theme)
    await openAWindow(page)

    // Edit mode + the gallery + a settings panel, all on screen at once so a
    // single scan covers every surface the rail can show.
    await page.getByRole('button', { name: 'Edit widgets' }).click()
    await page.getByRole('button', { name: 'Add widget' }).click()
    await expect(page.getByText('Add a widget')).toBeVisible()
    const galleryText = await page.locator('.vwidget-panel').innerText()
    expect(galleryText.length, 'the gallery rendered no text').toBeGreaterThan(50)

    let failures = await belowAA(page)
    expect(failures, `sub-AA text in the gallery (${theme}):\n${failures.join('\n')}`).toEqual([])

    await page.getByRole('button', { name: 'Configure World clock' }).click()
    await expect(page.getByLabel('Cities')).toBeVisible()
    // The settings panel carries the permission copy, which is the longest and
    // dimmest prose in the whole rail.
    await page.getByRole('button', { name: 'Close gallery' }).click().catch(() => {})
    await page.getByRole('button', { name: 'Configure Agenda' }).click()
    await expect(page.getByText(/Read your agenda/)).toBeVisible()

    failures = await belowAA(page)
    expect(failures, `sub-AA text in the settings panel (${theme}):\n${failures.join('\n')}`).toEqual([])
  })
}
