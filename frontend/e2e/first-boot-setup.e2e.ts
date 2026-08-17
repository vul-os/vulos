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
  // Pinned explicitly, because leaving it out is how this spec passed while the
  // box it describes did not work. Unmocked /api routes get `{}` from
  // mock-backend.js; `{}` has no `mode`, so Setup.tsx's mount effect took
  // neither branch and the wizard survived. A real box answers instance_ready,
  // which the effect read as "already set up" and handed back to AuthGate.
  'GET /api/setup/mode': json({ mode: 'instance_ready' }),
}

/**
 * Every value GET /api/setup/mode can return, from
 * backend/services/bootmode/bootmode.go (mirrored in src/lib/bootmode.ts and
 * checked across the two by TestModeStringsMatchFrontend), plus the answers a
 * box gives when something is wrong.
 *
 * The wizard's decision to run belongs to /api/setup/status alone, so NONE of
 * these may change it. Enumerating them is the point: two separate causes of
 * the same symptom shipped because a single-value fixture happened to miss the
 * one value a real machine returns.
 */
const MODE_ANSWERS: Array<[label: string, spec: unknown]> = [
  ['instance_ready (what a real first boot reports)', json({ mode: 'instance_ready' })],
  ['instance_absent', json({ mode: 'instance_absent' })],
  ['normal (retired name)', json({ mode: 'normal' })],
  ['setup (retired name, and the old fixture)', json({ mode: 'setup' })],
  ['{} (an unmocked endpoint)', json({})],
  ['500', json({ error: 'unavailable' }, 500)],
]

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

for (const [label, spec] of MODE_ANSWERS) {
  test(`the wizard runs whatever /api/setup/mode says — ${label}`, async ({ page }) => {
    test.setTimeout(90_000)
    await installBackend(page, { ...FIRST_BOOT, 'GET /api/setup/mode': spec })
    await page.goto('/')

    await expect(
      page.locator('.wz-root'),
      `the setup wizard did not render on a first boot (mode: ${label})`,
    ).toBeVisible({ timeout: 30_000 })

    await expect(
      page.getByText('Create your account'),
      `the login form rendered on a box with no accounts (mode: ${label})`,
    ).toHaveCount(0)
  })
}

test('setup status failing asks the user instead of guessing', async ({ page }) => {
  test.setTimeout(90_000)

  // CHANGED DELIBERATELY. This test used to pin the opposite behaviour, and the
  // reason for the flip is the whole point of the change.
  //
  // App.tsx defaulted setup_complete to TRUE whenever the probe failed:
  //   .then(r => r.ok ? r.json() : { setup_complete: true })
  //   .catch(() => setSetupDone(true))
  //
  // The old comment here called that "defensible — trapping someone in a wizard
  // they cannot leave is worse". What it missed is WHEN a probe fails: a box
  // that is still starting, which is the normal condition on a first boot. So
  // the fail-open path was not the rare case being tolerated for the sake of
  // the common one; on a first boot it was the likely case, and it skipped the
  // fifteen steps — timezone, network, identity keypair, SSH keys, recovery kit
  // — on the machine that had never had them.
  //
  // The other default is no better: re-running the wizard on a box that IS set
  // up fails at the account step on a duplicate username. Since neither guess is
  // safe, the shell now retries (4 attempts, ~7s — lib/setupProbe.ts) and then
  // asks the person in front of the machine, who knows which box this is.
  await installBackend(page, {
    ...FIRST_BOOT,
    'GET /api/setup/status': json({ error: 'unavailable' }, 500),
  })
  await page.goto('/')

  await expect(
    page.getByRole('button', { name: /run setup/i }),
    'setup status returned 500 and the shell did not offer the run-setup / sign-in choice',
  ).toBeVisible({ timeout: 40_000 })
  await expect(page.getByRole('button', { name: /go to sign-in/i })).toBeVisible()

  // It must not have quietly picked one on the way past. Both were reachable
  // failure modes of the code this replaces.
  await expect(page.getByText('Create your account')).toHaveCount(0)

  // And the choice must be real: taking it lands on the wizard.
  await page.getByRole('button', { name: /run setup/i }).click()
  await expect(
    page.getByRole('button', { name: /get started|begin|continue|next/i }).first(),
    'choosing "Run setup" after an unanswered probe did not open the wizard',
  ).toBeVisible({ timeout: 30_000 })
})
