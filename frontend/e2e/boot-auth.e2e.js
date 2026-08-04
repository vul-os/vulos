// boot-auth.e2e.js — real-browser boot + authentication flow.
//
// Drives the actual production bundle through the AuthGate branches:
//   • setup-complete + no user → the sign-in screen,
//   • first-boot (setup incomplete / no users) → the create-account screen,
//   • a bad-credentials login surfaces a friendly error and re-enables submit,
//   • a good login transitions past the login screen into the shell.

import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

test('shows the sign-in screen when a user exists but no session', async ({ page }) => {
  await installBackend(page, {
    'GET /api/auth/me': json({}, 401),
    'GET /api/auth/status': json({ has_users: true }),
  })
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await expect(page.getByPlaceholder('Username')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Sign In' })).toBeVisible()
})

test('first boot shows the create-account setup screen', async ({ page }) => {
  await installBackend(page, {
    'GET /api/auth/me': json({}, 401),
    'GET /api/auth/status': json({ has_users: false }),
    'GET /api/setup/status': json({ setup_complete: true }), // login-screen setup, not the wizard
  })
  await page.goto('/')
  await expect(page.getByText('Create your account')).toBeVisible()
  await expect(page.getByPlaceholder('Your name')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create Account' })).toBeVisible()
})

test('bad credentials surface a friendly error and keep the button usable', async ({ page }) => {
  await installBackend(page, {
    'GET /api/auth/me': json({}, 401),
    'GET /api/auth/status': json({ has_users: true }),
    'POST /api/auth/login': json({ error: 'Incorrect username or password.' }, 401),
  })
  await page.goto('/')
  await page.getByPlaceholder('Username').fill('ada')
  await page.getByPlaceholder('Password').fill('wrong')
  await page.getByRole('button', { name: 'Sign In' }).click()

  await expect(page.getByRole('alert')).toContainText(/Incorrect username or password/i)
  await expect(page.getByRole('button', { name: 'Sign In' })).toBeEnabled()
})

test('a successful login leaves the sign-in screen', async ({ page }) => {
  // /api/auth/me is 401 until login, then returns the profile so the shell mounts.
  let loggedIn = false
  await installBackend(page, {
    'GET /api/auth/me': () => loggedIn
      ? json({ user: { id: 'u1', username: 'ada' }, profile: { display_name: 'Ada' } })
      : json({}, 401),
    'GET /api/auth/status': json({ has_users: true }),
    'POST /api/auth/login': () => { loggedIn = true; return json({ ok: true }) },
  })
  await page.goto('/')
  await page.getByPlaceholder('Username').fill('ada')
  await page.getByPlaceholder('Password').fill('hunter2')
  await page.getByRole('button', { name: 'Sign In' }).click()

  // The sign-in heading goes away once AuthProvider re-fetches the user.
  await expect(page.getByRole('button', { name: 'Sign In' })).toBeHidden({ timeout: 10_000 })
})
