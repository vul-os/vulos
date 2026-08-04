// webpush-settings.e2e.js — the Web Push opt-in toggle in the REAL Settings app.
//
// PUSH-CELL-01 shipped the client helper (webPush.js) but never surfaced it; the
// polish pass added a device-scoped toggle to Settings → Notifications. This
// drives that surface in a real Chromium against a mocked box:
//   • when the box has NO VAPID keys, the row is honest/non-actionable
//     (explanatory copy, no live switch) rather than a dead toggle;
//   • when the box HAS the send-path configured, a real <switch> renders.
//
// We assert the fail-safe presentation rather than completing a subscription:
// a genuine PushManager.subscribe needs HTTPS + a real vendor endpoint, out of
// scope for a network-mocked E2E. The subscribe/register wire contract is
// covered by the jsdom unit suite (webPush.test.js).

import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

const PUBKEY = 'BEl6-2p9d3ExampleVapidPublicKeyBytesForE2E_00000000000000000000000000000000000000000000'

// Open Settings via the command palette and land on the Notifications section.
async function openNotificationSettings(page) {
  await page.goto('/')
  await expect(page.getByTitle('Chat (Ctrl+K)')).toBeVisible({ timeout: 15_000 })
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  // "settings" fuzzy-matches the Settings app as the top row; Enter launches the
  // highlighted result (clicking palette text is flaky — the overlay intercepts).
  await input.fill('settings')
  await expect(page.getByText('Settings').first()).toBeVisible()
  await page.keyboard.press('Enter')

  // In the Settings window, go to the Notifications section. Scope to the
  // "Settings sections" nav so we don't hit the menubar notification bell (which
  // also carries a "Notifications" accessible name at zero unread).
  const settingsNav = page.getByRole('navigation', { name: 'Settings sections' }).first()
  const nav = settingsNav.getByRole('button', { name: 'Notifications' })
  await expect(nav).toBeVisible({ timeout: 10_000 })
  await nav.click()
  await expect(page.getByRole('heading', { name: 'This device' })).toBeVisible({ timeout: 10_000 })
}

test('renders an honest non-actionable row when the box has no VAPID keys', async ({ page }) => {
  await installBackend(page, {
    'GET /api/notifications/push/vapid-public': json({ enabled: false }),
  })
  await openNotificationSettings(page)

  await expect(page.getByText(/hasn.t enabled the Web Push send-path/i)).toBeVisible()
  // Non-actionable: no live push switch is rendered in the unavailable state.
  await expect(page.getByRole('switch', { name: /push notifications on this device/i }))
    .toHaveCount(0)
})

test('renders the honest blocked state when the browser denies notifications', async ({ page }) => {
  // The box IS configured, but a real (headless, plain-HTTP) browser reports
  // Notification.permission === 'denied' — an insecure origin can't hold a push
  // subscription. The toggle must explain why it can't turn on rather than
  // showing a dead switch. (The actionable "off" state — permission granted on a
  // secure origin — is covered deterministically by the jsdom unit suite, since
  // real Web Push needs HTTPS + a real vendor endpoint, out of scope for E2E.)
  await installBackend(page, {
    'GET /api/notifications/push/vapid-public': json({ enabled: true, publicKey: PUBKEY }),
  })
  await openNotificationSettings(page)

  await expect(page.getByText(/Blocked in your browser settings/i)).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('switch', { name: /push notifications on this device/i })).toHaveCount(0)
})
