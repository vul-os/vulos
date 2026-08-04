// notifications.e2e.js — the notification center in the REAL shell chrome.
//
// The backend notification feed (GET /api/notifications) is folded into the
// client store by notificationBridge on boot. This drives the whole surface in
// a real browser: the menubar bell reflects the unread count, the center opens
// and lists the backend items, and "Mark all read" both clears the count AND
// syncs back to the server (POST /api/notifications/read). Complements the jsdom
// integration suite with a real-DOM, real-event pass.

import { test, expect } from '@playwright/test'
import { installBackend } from './mock-backend.js'

// Two unread backend notifications in the notify.Notification wire shape.
const FEED = [
  { id: 'n1', title: 'Invoice due', body: 'Acme Corp — $420', source: 'mail', level: 'info', read: false, created_at: '2026-07-07T10:00:00Z' },
  { id: 'n2', title: 'Backup completed', body: 'Nightly snapshot ok', source: 'system', level: 'info', read: false, created_at: '2026-07-07T09:00:00Z' },
]

async function boot(page, overrides = {}) {
  await installBackend(page, {
    'GET /api/notifications': { status: 200, contentType: 'application/json', body: JSON.stringify(FEED) },
    'GET /api/notifications/unread': { status: 200, contentType: 'application/json', body: JSON.stringify({ unread: 2 }) },
    ...overrides,
  })
  await page.goto('/')
  // Gate on the notification bell (in the always-on status bar, present in every
  // layout) rather than a layout-specific chrome element.
  await expect(page.getByRole('button', { name: /unread|^Notifications$/i }).first())
    .toBeVisible({ timeout: 15_000 })
}

test('the menubar bell reflects the backend unread count and opens the center', async ({ page }) => {
  await boot(page)

  // The bell aria-label reflects the hydrated unread count (2).
  const bell = page.getByRole('button', { name: /2 unread/i })
  await expect(bell).toBeVisible({ timeout: 10_000 })

  await bell.click()
  const panel = page.getByRole('dialog', { name: 'Notifications' })
  await expect(panel).toBeVisible()
  await expect(panel.getByText('Invoice due')).toBeVisible()
  await expect(panel.getByText('Backup completed')).toBeVisible()
})

test('"Mark all read" clears the count and syncs to the backend', async ({ page }) => {
  let readPosted = false
  await boot(page, {
    'POST /api/notifications/read': () => {
      readPosted = true
      return { status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) }
    },
  })

  await page.getByRole('button', { name: /2 unread/i }).click()
  await page.getByRole('button', { name: 'Mark all read' }).click()

  // The bell drops back to the no-count label. (The open panel also carries a
  // "Notifications" label, so scope to the first match — the menubar bell.)
  await expect(page.getByRole('button', { name: /^Notifications$/i }).first()).toBeVisible({ timeout: 10_000 })

  // And the mark-read synced to the server.
  await expect.poll(() => readPosted, { timeout: 5_000 }).toBe(true)
})
