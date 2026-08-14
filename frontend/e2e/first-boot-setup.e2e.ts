import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

/**
 * The first-boot wizard must actually appear on a machine that has not been
 * set up.
 *
 * NOTHING covered this. No e2e spec set `setup_complete: false`, so every test
 * in this directory ran against a box that claimed setup was already finished,
 * and the fifteen-step wizard — welcome, chooser, device, language, timezone,
 * network, account, pin, apps, appearance, identity, storage, ssh, recoverykit,
 * ready — was exercised by no gate at all.
 *
 * What that cost: build.sh created /var/lib/vulos/.setup-complete inside the
 * rootfs, so EVERY image shipped between 2026-03-31 and 2026-08-15, live and
 * installed, booted claiming setup was done. A first boot asked for a display
 * name, a username and a password and went to the desktop. No timezone, no
 * network, no identity keypair, no SSH keys, no recovery kit. Confirmed on a
 * pristine never-booted v0.2.0 image, which answered {"setup_complete":true}
 * before anyone touched it.
 *
 * The build side is guarded by TestBuildDoesNotPreCompleteSetup. This spec
 * guards the other half: that the shell still renders the wizard when the
 * backend says setup is outstanding. Both are needed — the marker being absent
 * means nothing if AuthGate has stopped honouring it.
 */

const FIRST_BOOT = {
  'GET /api/auth/me': json({}, 401),
  'GET /api/auth/status': json({ has_users: false }),
  'GET /api/setup/status': json({ setup_complete: false }),
}

test('a machine that has not been set up shows the setup wizard, not the login form', async ({ page }) => {
  test.setTimeout(90_000)
  await installBackend(page, FIRST_BOOT)
  await page.goto('/')

  // The wizard's own first step. Asserted on a control the LoginScreen does not
  // have, rather than on absence of the login form: "no login form" would also
  // be satisfied by a blank page or a crash, and this defect rendered a
  // perfectly good login form, so absence-of is exactly the wrong assertion.
  await expect(
    page.getByRole('button', { name: /get started|begin|continue|next/i }).first(),
    'the setup wizard did not render on a box reporting setup_complete=false',
  ).toBeVisible({ timeout: 30_000 })

  // And specifically NOT the create-account form, which is what shipped images
  // showed instead. Both directions matter: the wizard appearing is the fix,
  // the login form not appearing is the regression.
  await expect(page.getByText('Create your account')).toHaveCount(0)
})

test('setup status failing does not silently skip the wizard on a fresh box', async ({ page }) => {
  test.setTimeout(90_000)

  // App.tsx defaults setup_complete to TRUE when the request fails:
  //   .then(r => r.ok ? r.json() : { setup_complete: true })
  //   .catch(() => setSetupDone(true))
  //
  // That is fail-open on the one screen where failing open means the user
  // never sets up their machine. It is defensible — trapping someone in a
  // wizard they cannot leave is worse — but it MUST be a deliberate, visible
  // choice rather than an accident, because it is a second way for the wizard
  // to vanish on a real box with a slow or unhealthy backend.
  //
  // This test pins the CURRENT behaviour so a change is a decision. If the
  // product decides fail-closed is right, this expectation flips and the
  // comment above should say why.
  await installBackend(page, {
    ...FIRST_BOOT,
    'GET /api/setup/status': json({ error: 'unavailable' }, 500),
  })
  await page.goto('/')

  await expect(
    page.getByText('Create your account'),
    'setup status returned 500 and the shell rendered something other than the ' +
      'documented fail-open create-account screen — behaviour changed, update this test deliberately',
  ).toBeVisible({ timeout: 30_000 })
})
