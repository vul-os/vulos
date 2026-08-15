/**
 * settings-shots.mjs — render Settings and write PNGs so they can be LOOKED at.
 *
 * Not a test. Every defect worth finding tonight survived a green unit suite,
 * eslint and tsc and was caught only by opening the image. Run:
 *   E2E_PORT=4188 node e2e/settings-shots.mjs
 */
import { chromium } from 'playwright'
import { mkdirSync } from 'node:fs'
// The same mocked box the e2e suite uses. A blanket `{}` for every /api route
// is NOT enough: boot asks /api/setup/status and /api/auth/status for real
// shapes, and an empty object sends the shell to "This box did not say whether
// it has been set up" — which is what the first run of this script rendered at
// all seven widths.
import { installBackend } from './mock-backend.js'

const PORT = process.env.E2E_PORT || 4188
const BASE = `http://localhost:${PORT}`
const OUT = process.env.SHOT_DIR || 'e2e-shots/settings'
const WIDTHS = [320, 390, 428, 768, 834, 1024, 1440]
const SECTIONS = process.env.SHOT_SECTIONS
  ? process.env.SHOT_SECTIONS.split(',')
  : ['WiFi', 'Sound', 'Display', 'Backup & Sync', 'Users & Profiles', 'About']

mkdirSync(OUT, { recursive: true })

const browser = await chromium.launch()

for (const theme of ['dark', 'light']) {
  for (const width of WIDTHS) {
    const ctx = await browser.newContext({ viewport: { width, height: 900 } })
    const page = await ctx.newPage()
    await page.addInitScript((t) => {
      try {
        localStorage.setItem('vulos-theme', t)
        localStorage.setItem('vulos-ai-firstrun-done', '1')
      } catch { /* private mode */ }
    }, theme)
    await installBackend(page)
    await page.goto(BASE)
    await page.waitForTimeout(2500)
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)

    // Open Settings through the command palette (the real launch path).
    try {
      await page.keyboard.press('Meta+k')
      await page.waitForTimeout(600)
      await page.fill('input[placeholder*="Search apps"]', 'settings')
      await page.waitForTimeout(400)
      await page.keyboard.press('Enter')
      await page.waitForTimeout(1600)
    } catch { /* palette not up at this width; shoot what is there */ }

    await page.screenshot({ path: `${OUT}/${theme}-${width}-open.png` })

    for (const label of SECTIONS) {
      try {
        if (width < 640) {
          const opener = page.getByRole('button', { name: 'Open settings sections' })
          if (await opener.count() && (await opener.getAttribute('aria-expanded')) !== 'true') {
            await opener.click()
            await page.waitForTimeout(300)
          }
        }
        const nav = page.getByRole('navigation', { name: 'Settings sections' })
        const btn = nav.getByRole('button', { name: label, exact: true }).first()
        if (!(await btn.count())) continue
        await btn.click({ timeout: 4000 })
        await page.waitForTimeout(500)
        const slug = label.replace(/[^a-z0-9]+/gi, '-').toLowerCase()
        await page.screenshot({ path: `${OUT}/${theme}-${width}-${slug}.png` })
      } catch { /* keep going: a missing section is reported by the sweep, not here */ }
    }
    await ctx.close()
  }
}

await browser.close()
console.log(`wrote shots to ${OUT}`)
