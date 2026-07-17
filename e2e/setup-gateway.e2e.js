// setup-gateway.e2e.js — GATEWAY-01 first-boot gateway choice, in a real browser.
//
// Drives the install wizard to the account step, chooses "Connect Vulos Cloud",
// expands the advanced control-plane panel, switches to "Use my own gateway",
// enters a URL, and connection-tests it against a mocked /api/gateway/check.
// A zero-pageerror guard fails the run on any uncaught exception along the way.

import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

test('first-boot: choose a self-hosted gateway and connection-test it', async ({ page }) => {
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(e.message))

  let checkedURL = null
  await installBackend(page, {
    // Force the first-boot wizard: setup incomplete, no users, not signed in.
    'GET /api/setup/status': json({ setup_complete: false }),
    'GET /api/auth/status': json({ has_users: false }),
    'GET /api/auth/me': () => json({}, 401),
    // Not cloud-enrolled → the account step defaults to the local/pick view.
    'GET /api/auth/cloud/status': json({ enrolled: false }),
    // Gateway surface: current is the default, and the candidate is reachable.
    'GET /api/gateway': json({ url: 'https://api.vulos.org', source: 'default', default: 'https://api.vulos.org', env_url: '', configured: '' }),
    'POST /api/gateway/check': (req) => {
      try { checkedURL = JSON.parse(req.postData() || '{}').url } catch { /* noop */ }
      return json({ ok: true, url: 'https://cp.example.org' })
    },
  })

  await page.goto('/')

  // 1. Welcome → New System.
  await page.getByRole('button', { name: 'Get Started' }).click()
  await page.getByRole('button', { name: /New System/ }).click()

  // 2. Click through the intermediate steps (device, language, timezone,
  //    network) via whichever primary nav button is shown, until the account
  //    choice ("How do you want to manage this device?") appears.
  const accountHeading = page.getByRole('heading', { name: /How do you want to manage this device/i })
  await expect(async () => {
    if (await accountHeading.isVisible().catch(() => false)) return
    const next = page.getByRole('button', { name: /(Continue|Skip)\s*→/ }).last()
    await next.click({ timeout: 1000 })
    await expect(accountHeading).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 20_000 })

  // 3. Choose "Connect Vulos Cloud" → the cloud sign-in form.
  await page.getByRole('button', { name: /Connect Vulos Cloud/ }).click()

  // 4. Expand the advanced control-plane panel and switch to a custom gateway.
  await page.getByRole('button', { name: /Control plane — using Vulos Cloud/i }).click()
  await page.getByRole('button', { name: 'Use my own gateway' }).click()

  // 5. Enter the gateway URL and run the connection test.
  await page.getByLabel('Gateway URL').fill('https://cp.example.org')
  await page.getByRole('button', { name: 'Test' }).click()

  // 6. The reachable confirmation appears and the backend was probed.
  await expect(page.getByText(/Gateway reachable/i)).toBeVisible({ timeout: 10_000 })
  expect(checkedURL).toBe('https://cp.example.org')

  expect(pageErrors, `uncaught page errors:\n${pageErrors.join('\n')}`).toEqual([])
})
