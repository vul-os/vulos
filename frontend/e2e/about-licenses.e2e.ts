// about-licenses.e2e.js — the "Open source licences" surface in Settings → About.
//
// LICENCE COMPLIANCE: the OS must actually SHOW the third-party notices it is
// obliged to carry, and the GPL/LGPL written offer. The backend serves both from
// /opt/vulos/legal via GET /api/system/licenses and /api/system/written-offer;
// the About panel fetches and displays them. This drives that real surface in a
// Chromium against a mocked box, asserting:
//   • the licences panel opens and shows the fetched notices text;
//   • the written-offer panel opens and shows the DRAFT offer text;
//   • no page error is thrown while doing so.

import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

const NOTICES = '# Third-Party Notices\n\nGo modules: 87 · npm packages: 83\n\n### leaflet 1.9.4\n- Licence: BSD-2-Clause\n'
const OFFER = '# Written Offer for Source Code\n\nSTATUS: DRAFT — REQUIRES FOUNDER AND LAWYER REVIEW.\n'

function text(body: string) {
  return { status: 200, contentType: 'text/plain; charset=utf-8', body }
}

async function openAbout(page: Page) {
  await page.goto('/')
  await expect(page.getByTitle('Chat (Ctrl+K)')).toBeVisible({ timeout: 15_000 })
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill('settings')
  await expect(page.getByText('Settings').first()).toBeVisible()
  await page.keyboard.press('Enter')

  const settingsNav = page.getByRole('navigation', { name: 'Settings sections' }).first()
  const nav = settingsNav.getByRole('button', { name: 'About' })
  await expect(nav).toBeVisible({ timeout: 10_000 })
  await nav.click()
  await expect(page.getByRole('heading', { name: 'Legal' })).toBeVisible({ timeout: 10_000 })
}

test('About surfaces the open-source licences and the written offer', async ({ page }) => {
  const pageErrors: string[] = []
  page.on('pageerror', (e) => pageErrors.push(e.message))

  await installBackend(page, {
    'GET /api/system/licenses': text(NOTICES),
    'GET /api/system/written-offer': text(OFFER),
  })
  await openAbout(page)

  // Open the licences panel and confirm the fetched notices render.
  await page.getByRole('button', { name: /Open source licences/i }).click()
  await expect(page.getByText(/Licence: BSD-2-Clause/)).toBeVisible({ timeout: 10_000 })

  // Open the written offer and confirm the DRAFT text renders.
  await page.getByRole('button', { name: /Source code for GPL\/LGPL components/i }).click()
  await expect(page.getByText(/REQUIRES FOUNDER AND LAWYER REVIEW/)).toBeVisible({ timeout: 10_000 })

  expect(pageErrors, `page errors: ${pageErrors.join(' | ')}`).toHaveLength(0)
})

test('About shows an honest message when notices are unavailable', async ({ page }) => {
  await installBackend(page, {
    'GET /api/system/licenses': { status: 404, contentType: 'text/plain', body: 'not found' },
  })
  await openAbout(page)

  await page.getByRole('button', { name: /Open source licences/i }).click()
  await expect(page.getByText(/not available on this system/i)).toBeVisible({ timeout: 10_000 })
})
