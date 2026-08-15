import { test, expect, type Page } from '@playwright/test'
import { bootActivity, maximise, APP } from './activity-harness'
import { belowAA } from './contrast-scan'

/**
 * Geometry and contrast for the Activity Monitor, measured on composited
 * pixels in BOTH themes and at phone width.
 *
 * The overflow check deliberately measures all four edges. A parallel agent's
 * viewport test passed while a rail rendered 350px off the bottom of the
 * screen, because the test checked left and right and the rail grew DOWN.
 */

const SHOTS = 'e2e-shots/activity'

const PRESETS = [
  { id: 'phone', w: 390, h: 844 },
  { id: 'tablet', w: 834, h: 1112 },
  { id: 'desktop', w: 1440, h: 900 },
] as const

/** Any element inside the app whose box escapes the viewport, on ANY edge. */
async function overflowing(page: Page) {
  return page.evaluate(() => {
    const root = document.querySelector('[data-app="activity"]')
    if (!root) return ['activity app is not mounted']
    const vw = window.innerWidth, vh = window.innerHeight
    const bad: string[] = []
    for (const el of Array.from(root.querySelectorAll('*'))) {
      const r = el.getBoundingClientRect()
      if (r.width === 0 || r.height === 0) continue
      const style = getComputedStyle(el)
      if (style.visibility === 'hidden' || style.display === 'none') continue
      // A scroll container legitimately holds content wider/taller than
      // itself; only the container's OWN box is checked for those.
      const overflowsLeft = r.left < -1
      const overflowsRight = r.right > vw + 1
      const overflowsTop = r.top < -1
      const overflowsBottom = r.bottom > vh + 1
      if (overflowsLeft || overflowsRight || overflowsTop || overflowsBottom) {
        // Walk up looking for a scroll container that clips it.
        let p: Element | null = el.parentElement
        let clipped = false
        while (p && p !== document.body) {
          const ps = getComputedStyle(p)
          if (/(auto|scroll|hidden)/.test(ps.overflow + ps.overflowX + ps.overflowY)) { clipped = true; break }
          p = p.parentElement
        }
        if (clipped) continue
        const edges = [
          overflowsLeft && `left ${Math.round(r.left)}`,
          overflowsRight && `right ${Math.round(r.right)} > ${vw}`,
          overflowsTop && `top ${Math.round(r.top)}`,
          overflowsBottom && `bottom ${Math.round(r.bottom)} > ${vh}`,
        ].filter(Boolean).join(', ')
        bad.push(`${el.tagName.toLowerCase()}.${String(el.className).slice(0, 40)} — ${edges}`)
      }
    }
    return bad.slice(0, 10)
  })
}

for (const theme of ['dark', 'light'] as const) {
  test(`meets WCAG AA on the ${theme} theme`, async ({ page }) => {
    test.setTimeout(180_000)
    await page.setViewportSize({ width: 1440, height: 900 })
    await bootActivity(page, { theme })
    await maximise(page)

    // Every tab, because a contrast gate that only ever sees the first one is
    // measuring a third of the app.
    for (const tab of [/^Processes/, /^Apps/, /^Network/]) {
      await page.locator(APP).getByRole('button', { name: tab }).click()
      await page.waitForTimeout(500)
      expect(await belowAA(page), `${theme} theme, ${String(tab)} tab`).toEqual([])
      await page.screenshot({ path: `${SHOTS}/${theme}-${String(tab).replace(/[^a-z]/gi, '')}.png` })
    }
  })

  test(`opens a confirmation that meets AA on the ${theme} theme`, async ({ page }) => {
    test.setTimeout(180_000)
    await page.setViewportSize({ width: 1440, height: 900 })
    await bootActivity(page, { theme })
    await maximise(page)
    const app = page.locator(APP)
    await app.getByText('gimp', { exact: true }).first().click()
    await app.getByRole('button', { name: 'Force Quit' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.waitForTimeout(300)
    expect(await belowAA(page), `${theme} force-quit dialog`).toEqual([])
    await page.screenshot({ path: `${SHOTS}/${theme}-confirm.png` })
  })
}

for (const p of PRESETS) {
  test(`fits ${p.id} (${p.w}x${p.h}) without escaping the viewport`, async ({ page }) => {
    test.setTimeout(180_000)
    await page.setViewportSize({ width: p.w, height: p.h })
    await bootActivity(page)
    await maximise(page)

    const app = page.locator(APP)
    await expect(app).toBeVisible()

    // The app itself must sit inside the viewport on all four edges.
    const box = await app.boundingBox()
    expect(box, 'activity app has no box').not.toBeNull()
    if (box) {
      expect(box.x, 'app starts left of the viewport').toBeGreaterThanOrEqual(-1)
      expect(box.y, 'app starts above the viewport').toBeGreaterThanOrEqual(-1)
      expect(box.x + box.width, 'app extends past the right edge').toBeLessThanOrEqual(p.w + 1)
      expect(box.y + box.height, 'app extends past the BOTTOM edge').toBeLessThanOrEqual(p.h + 1)
    }

    expect(await overflowing(page), `${p.id}: elements escaping the viewport`).toEqual([])

    // The page body must never scroll sideways — wide content scrolls inside
    // its own container.
    const bodyScroll = await page.evaluate(() => ({
      x: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      y: document.body.scrollWidth - window.innerWidth,
    }))
    expect(bodyScroll.x, `${p.id}: the page scrolls horizontally`).toBeLessThanOrEqual(1)

    await page.screenshot({ path: `${SHOTS}/fit-${p.id}.png`, fullPage: false })
  })
}

test('at phone width the toolbar, tabs and table are all still usable', async ({ page }) => {
  test.setTimeout(180_000)
  await page.setViewportSize({ width: 390, height: 844 })
  await bootActivity(page)
  await maximise(page)
  const app = page.locator(APP)

  // Tabs stay reachable — they scroll rather than wrapping into the graphs.
  for (const tab of [/^Processes/, /^Apps/, /^Network/]) {
    await expect(app.getByRole('button', { name: tab })).toBeVisible()
  }
  await expect(app.getByLabel('Filter rows')).toBeVisible()

  // A row is selectable and the action bar responds, at the width where the
  // action bar has the least room.
  await app.getByText('gimp', { exact: true }).first().click()
  await expect(app.getByRole('button', { name: 'Force Quit' })).toBeEnabled()

  await page.screenshot({ path: `${SHOTS}/phone-selected.png` })
})
