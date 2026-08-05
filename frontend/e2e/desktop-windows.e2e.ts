// desktop-windows.e2e.js — real-browser desktop + window management.
//
// Boots into the shell, launches an app from the Launchpad, and drives the
// window lifecycle through the real compositor UI: a window opens with its
// title-bar traffic lights + a Dock entry; minimize sends it to the Dock;
// clicking the Dock entry restores it; close removes it. Also opens Mission
// Control (F3) and the ⌘K palette to confirm the shell chrome is wired.

import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

async function boot(page: Page) {
  await installBackend(page)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
}

// Launch an app through the ⌘K command palette (the same real launch path the
// Launchpad uses via launchApp), which is more click-stable in a headless run
// than the re-rendering Launchpad grid.
async function launch(page: Page, appName: string) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(appName)
  // The matching app row activates on Enter (the top-ranked fuzzy match).
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
}

test('launching an app opens a window with title-bar controls and a Dock entry', async ({ page }) => {
  await boot(page)
  await launch(page, 'Calculator')

  // Window chrome: the traffic-light controls carry accessible names.
  await expect(page.getByRole('button', { name: 'Close window' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Minimize window' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Maximize window' }).first()).toBeVisible()

  // The Dock now has an entry for the running app.
  await expect(page.getByRole('button', { name: 'Calculator' })).toBeVisible()
})

test('minimize sends the window to the Dock; clicking the Dock entry restores it', async ({ page }) => {
  await boot(page)
  await launch(page, 'Calculator')
  await expect(page.getByRole('button', { name: 'Calculator' })).toBeVisible()

  await page.getByRole('button', { name: 'Minimize window' }).first().click()
  // The Dock item is now labelled minimized.
  const minimized = page.getByRole('button', { name: 'Calculator (minimized)' })
  await expect(minimized).toBeVisible()

  // Restore from the Dock.
  await minimized.click()
  await expect(page.getByRole('button', { name: 'Calculator', exact: true })).toBeVisible()
})

test('closing the window removes it from the Dock', async ({ page }) => {
  await boot(page)
  await launch(page, 'Calculator')
  await expect(page.getByRole('button', { name: 'Calculator' })).toBeVisible()

  await page.getByRole('button', { name: 'Close window' }).first().click()
  await expect(page.getByRole('button', { name: 'Calculator' })).toBeHidden()
})

test('Mission Control opens with F3 and closes with Escape', async ({ page }) => {
  await boot(page)
  await launch(page, 'Calculator')
  await page.keyboard.press('F3')
  // The add-desktop affordance is unique to Mission Control.
  await expect(page.getByRole('button', { name: 'Add desktop' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: 'Add desktop' })).toBeHidden()
})
