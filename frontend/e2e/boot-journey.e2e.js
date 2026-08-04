// boot-journey.e2e.js — the full first-surface journey in ONE real-browser run,
// guarded end-to-end by a zero-pageerror assertion.
//
//   boot (unauthenticated) → sign-in screen
//   → login → desktop shell mounts
//   → launch an app from the ⌘K palette → a real window + Dock entry
//
// A single `page.on('pageerror')` collector is attached BEFORE navigation and
// asserted empty at the very end, so any uncaught exception thrown anywhere
// along the boot/login/desktop/app-launch path fails the test. This is the
// shell's regression guard against a broken production bundle.

import { test, expect } from '@playwright/test'
import { installBackend, json, PROFILE } from './mock-backend.js'

test('boot → login → desktop → app-launch runs with zero uncaught page errors', async ({ page }) => {
  // Hard guard: any uncaught exception anywhere in the journey fails the test.
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(e.message))

  // Stateful auth: /api/auth/me is 401 until POST /login flips it, so the shell
  // starts on the sign-in screen and mounts the desktop only after a real login.
  let loggedIn = false
  await installBackend(page, {
    'GET /api/auth/me': () => (loggedIn ? json(PROFILE) : json({}, 401)),
    'GET /api/auth/status': json({ has_users: true }),
    'GET /api/setup/status': json({ setup_complete: true }),
    'POST /api/auth/login': () => {
      loggedIn = true
      return json({ ok: true })
    },
  })

  // 1. Boot unauthenticated → sign-in screen.
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()

  // 2. Log in with valid credentials → the sign-in screen goes away.
  await page.getByPlaceholder('Username').fill('ada')
  await page.getByPlaceholder('Password').fill('hunter2')
  await page.getByRole('button', { name: 'Sign In' }).click()
  await expect(page.getByRole('button', { name: 'Sign In' })).toBeHidden({ timeout: 10_000 })

  // 3. The desktop shell mounts (the Launchpad/Applications control is present).
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })

  // 4. Launch an app through the ⌘K command palette (the real launch path).
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill('Calculator')
  await expect(page.getByText('Calculator', { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')

  // A real window opened: traffic-light controls + a Dock entry for the app.
  await expect(page.getByRole('button', { name: 'Close window' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Calculator' })).toBeVisible()

  // 5. End-to-end guard: not a single uncaught exception along the way.
  expect(pageErrors, `uncaught page errors: ${pageErrors.join(' | ')}`).toHaveLength(0)
})
