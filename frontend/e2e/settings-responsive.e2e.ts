/**
 * settings-responsive.e2e.ts — Settings must fit the screen it is on.
 *
 * Settings is 30+ sections and had never been walked below a desktop width. A
 * parallel agent found 88 horizontal overflows at 320px in the setup wizard and
 * sub-24px touch targets throughout the OS; tablets were getting 12x12px window
 * controls. This sweep walks every section at every width that matters and
 * fails on content that paints past the right edge.
 *
 * The widths are the real device classes, not round numbers:
 *   320  iPhone SE / smallest phone still in use
 *   390  iPhone 14/15
 *   428  iPhone Pro Max
 *   768  iPad portrait — the `sm:` rail/drawer boundary sits below this
 *   834  iPad Air portrait
 *   1024 iPad landscape / small laptop
 *   1440 desktop
 */
import { test, expect } from '@playwright/test'
import { openSettings, gotoSection, overflowsAt, smallTargets, SECTIONS } from './settings-harness.js'

const WIDTHS = [320, 390, 428, 768, 834, 1024, 1440]

// These sweeps genuinely visit 34 panels apiece, and below the `sm:` breakpoint
// each visit also opens the drawer — around a second of real work per section.
// At the 30s default they sat exactly on the boundary and tipped over under
// host load, reporting a timeout that reads like an overflow failure. The work
// is real, so the budget is raised rather than the sweep narrowed.
const SWEEP_TIMEOUT = 180_000

for (const width of WIDTHS) {
  test(`no horizontal overflow anywhere in Settings at ${width}px`, async ({ page }) => {
    test.setTimeout(SWEEP_TIMEOUT)
    await page.setViewportSize({ width, height: 900 })
    await openSettings(page)

    const failures: string[] = []
    let visited = 0
    for (const { label } of SECTIONS) {
      if (!(await gotoSection(page, label))) continue
      visited++
      for (const o of await overflowsAt(page)) {
        failures.push(`${label}: <${o.tag} class="${o.cls}"> "${o.text}" right=${o.right} > vw=${o.vw}`)
      }
    }

    // A sweep that navigated nowhere would report zero overflows and read as a
    // pass — this is the guard that makes the assertion above mean something.
    expect(visited, `walked too few sections at ${width}px to have measured anything`).toBeGreaterThan(8)
    expect(failures.sort()).toEqual([])
  })
}

test('every interactive control in Settings meets the 24px touch minimum at 390px', async ({ page }) => {
  test.setTimeout(SWEEP_TIMEOUT)
  await page.setViewportSize({ width: 390, height: 844 })
  await openSettings(page)

  const failures: string[] = []
  let visited = 0
  for (const { label } of SECTIONS) {
    if (!(await gotoSection(page, label))) continue
    visited++
    for (const t of await smallTargets(page)) {
      failures.push(`${label}: <${t.tag}> "${t.name}" ${t.w}x${t.h}`)
    }
  }
  expect(visited).toBeGreaterThan(8)
  expect([...new Set(failures)].sort()).toEqual([])
})

test('the section rail is reachable on a phone', async ({ page }) => {
  // The whole sweep above depends on being able to change section at 320px. If
  // the drawer opener regresses, every width test would quietly measure only
  // the default panel and still pass, so this pins the mechanism itself.
  await page.setViewportSize({ width: 320, height: 720 })
  await openSettings(page)
  expect(await gotoSection(page, 'About')).toBe(true)
  // The About panel's heading is the product name, not the nav label — asserting
  // on "About" matched the nav button and would have passed without the panel
  // ever rendering.
  await expect(page.getByRole('heading', { name: 'Vulos OS' }).first()).toBeVisible()
})
