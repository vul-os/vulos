import { test, expect } from '@playwright/test'
import { installBackend } from './mock-backend.js'

/**
 * The screen indicator appears on a multi-screen kiosk and NOWHERE else.
 *
 * vulos-kiosk sets screen/screens/screenIndex on the URL it opens (build.sh),
 * and the shell renders the output's name so a user can tell which monitor
 * they are looking at. The half that matters for safety is the NEGATIVE case:
 * `screens=1` is what every real boot uses today and what every hand-opened
 * tab and phone on the LAN gets, and it must render exactly what it rendered
 * before this feature existed.
 *
 * That was verified by eye when the launcher landed — two screenshots, one
 * with a chip and one without. This pins it, because "looks the same" is not
 * a property a screenshot can defend six months later.
 */
const SELECTOR = 'text=/^Screen \\d+ · /'

test('screens=2 shows the output name in the top bar', async ({ page }) => {
  await installBackend(page, {})
  await page.goto('/?screen=DP-2&screens=2&screenIndex=2')
  await expect(page.locator(SELECTOR)).toBeVisible()
  await expect(page.locator(SELECTOR)).toContainText('DP-2')
})

test('screens=1 shows nothing — the single-screen path is unchanged', async ({ page }) => {
  await installBackend(page, {})
  await page.goto('/?screen=HDMI-A-1&screens=1&screenIndex=1')
  // Anchor on the trust badge, which renders in the top bar on every shell
  // boot (confirmed in the rendered screenshots). Wait for it before asserting
  // an ABSENCE, or this passes
  // against a page that never rendered — the vacuity failure this suite keeps
  // finding elsewhere.
  await expect(page.getByText('ON DEVICE').first()).toBeVisible()
  await expect(page.locator(SELECTOR)).toHaveCount(0)
})

test('no parameters at all shows nothing — an ordinary tab', async ({ page }) => {
  await installBackend(page, {})
  await page.goto('/')
  await expect(page.getByText('ON DEVICE').first()).toBeVisible()
  await expect(page.locator(SELECTOR)).toHaveCount(0)
})

test('an inconsistent identity is refused rather than half-rendered', async ({ page }) => {
  await installBackend(page, {})
  // index > total, with total > 1 so the refusal is what does the work.
  //
  // The first version used screens=1&screenIndex=3 and was REDUNDANT: with
  // total=1 the chip is suppressed by isMultiScreen regardless, so deleting the
  // consistency check entirely left this green. Caught by mutation. With
  // screens=2 an unrefused identity renders "Screen 5 · DP-2", so the
  // assertion now depends on the rule it claims to cover.
  await page.goto('/?screen=DP-2&screens=2&screenIndex=5')
  await expect(page.getByText('ON DEVICE').first()).toBeVisible()
  await expect(page.locator(SELECTOR)).toHaveCount(0)
})

// The WINDOW TITLE is what lets labwc place each browser on its own output:
// a windowRule matches the title and applies MoveToOutput. If two instances
// share a title, one rule matches both and they land on the same monitor. So
// this is a compositor contract, asserted in a real browser rather than in a
// pure function only.
test('each screen gets a distinct window title for the compositor to match', async ({ page }) => {
  await installBackend(page, {})
  await page.goto('/?screen=HDMI-A-1&screens=2&screenIndex=1')
  await expect(page.getByText('ON DEVICE').first()).toBeVisible()
  await expect(page).toHaveTitle(/HDMI-A-1$/)

  await page.goto('/?screen=DP-2&screens=2&screenIndex=2')
  await expect(page.getByText('ON DEVICE').first()).toBeVisible()
  await expect(page).toHaveTitle(/DP-2$/)
})

test('a single-screen kiosk keeps the plain title', async ({ page }) => {
  await installBackend(page, {})
  await page.goto('/?screen=HDMI-A-1&screens=1&screenIndex=1')
  await expect(page.getByText('ON DEVICE').first()).toBeVisible()
  await expect(page).not.toHaveTitle(/HDMI-A-1/)
})
