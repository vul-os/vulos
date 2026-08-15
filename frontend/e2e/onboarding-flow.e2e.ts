import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import {
  bootWizard, walkWizard, advance, settle, STEP_IDS, FIRST_BOOT, RECOVERY_PHRASE,
  type StepId,
} from './onboarding-walk.js'

/**
 * What the first-boot wizard actually DOES — as opposed to what it renders.
 *
 * The whole defect class this file guards is steps that look like they worked.
 * Walking the wizard by hand against a backend gating exactly the paths the
 * real one gates found that four of the fifteen steps sent nothing at all on a
 * real first boot and could not report it:
 *
 *   - POST /api/auth/register did not run until the LAST step, so the six steps
 *     between the account form and it had no session.
 *   - identity, storage and ssh write to endpoints that are NOT in the
 *     backend's publicPaths (services/auth/handlers.go), so all of them 401'd.
 *   - Every call site was `try { await fetch(...) } catch {}` with no res.ok
 *     check. `fetch` rejects only on a network-level failure — a 401 RESOLVES —
 *     so the catch never ran and the wizard advanced.
 *
 * A rendering test cannot see any of that: those steps rendered beautifully.
 * These tests assert on the REQUESTS and on whether the wizard MOVES.
 */

test('the wizard has exactly the fifteen steps it claims, and the walker knows them all', async ({ page }) => {
  test.setTimeout(240_000)
  await bootWizard(page, 'dark')

  // Cross-check against the app's OWN count rather than against another copy of
  // the list: the progressbar's aria-valuemax is rendered from the step array
  // Setup.tsx actually walks. A list that drifted from the wizard would show up
  // here instead of silently under-measuring in every other spec.
  const max = await page.getByRole('progressbar').getAttribute('aria-valuemax')
  expect(Number(max), 'the wizard renders a different number of steps than STEP_IDS names').toBe(STEP_IDS.length)
  expect(STEP_IDS.length).toBe(15)
})

test('the account is created BEFORE the steps that need a session', async ({ page }) => {
  test.setTimeout(240_000)
  const { seen } = await installBackend(page, FIRST_BOOT)
  await page.addInitScript(() => document.documentElement.setAttribute('data-theme', 'dark'))
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/')
  await expect(page.getByRole('button', { name: /get started/i })).toBeVisible({ timeout: 30_000 })

  await walkWizard(page, async () => {})

  const at = (key: string) => seen.indexOf(key)
  expect(at('POST /api/auth/register'), 'the wizard never registered the owner account').toBeGreaterThanOrEqual(0)

  // THE ordering assertion. Each of these writes was previously sent with no
  // session at all, and answered 401 in silence.
  for (const gated of [
    'POST /api/identity/hostname',
    'POST /api/setup/storage',
    'PUT /api/storagemode',
    'POST /api/ssh/authorized',
  ]) {
    if (at(gated) < 0) continue // not every walk touches every optional write
    expect(
      at(gated),
      `${gated} was sent BEFORE POST /api/auth/register — it has no session and the real backend 401s it`,
    ).toBeGreaterThan(at('POST /api/auth/register'))
  }
})

/**
 * A rejected write must STOP the wizard.
 *
 * Table-driven over the three steps that write to session-gated endpoints,
 * because the previous behaviour was identical in all three and fixing one says
 * nothing about the others. Each case walks to its step, makes the box reject
 * the write, and asserts the wizard is still on that step afterwards.
 */
const REJECTING = [
  {
    step: 'identity' as StepId,
    route: 'POST /api/identity/hostname',
    // The hostname is only sent when the user edits it.
    act: async (page: import('@playwright/test').Page) => {
      await page.locator('#wz-hostname').fill('my-box')
      await settle(page)
      await page.getByRole('button', { name: /continue/i }).first().click()
    },
    stillThere: /Your Node Identity/i,
    says: /could not set the hostname/i,
  },
  {
    step: 'storage' as StepId,
    route: 'POST /api/setup/storage',
    act: async (page: import('@playwright/test').Page) => {
      await page.getByRole('button', { name: /continue/i }).first().click()
    },
    stillThere: /Cluster Storage/i,
    says: /could not save your storage settings/i,
  },
  {
    step: 'ssh' as StepId,
    route: 'POST /api/ssh/authorized',
    act: async (page: import('@playwright/test').Page) => {
      await page.getByRole('button', { name: /generate an ed25519 keypair/i }).first().click()
      await expect(page.getByText(/^SHA256:/)).toBeVisible({ timeout: 20_000 })
      await page.locator('input[type=checkbox]').first().check({ force: true })
      await settle(page)
      await page.getByRole('button', { name: /continue/i }).first().click()
    },
    stillThere: /Remote access over SSH/i,
    says: /did not accept the key/i,
  },
]

for (const c of REJECTING) {
  test(`a rejected write on the ${c.step} step stops the wizard and says so`, async ({ page }) => {
    test.setTimeout(240_000)
    await bootWizard(page, 'dark', { [c.route]: json({ error: 'unauthorized' }, 401) })

    for (const step of STEP_IDS) {
      if (step === c.step) break
      await settle(page)
      await advance(page, step)
    }
    await settle(page)

    await c.act(page)
    await settle(page)

    // Both halves matter. "Shows an error" alone would pass on a wizard that
    // flashed a message and advanced anyway; "did not advance" alone would pass
    // on a wizard that froze silently.
    await expect(
      page.getByText(c.stillThere),
      `the wizard advanced past ${c.step} even though ${c.route} was rejected`,
    ).toBeVisible()
    await expect(
      page.getByText(c.says),
      `${c.step} did not tell the user that ${c.route} failed`,
    ).toBeVisible()
  })
}

test('the downloaded recovery kit contains the recovery phrase', async ({ page }) => {
  // The kit used to hold a ULID, a hostname, an SSH fingerprint and a checksum
  // of itself — four identifiers and no credential — while making the user type
  // "confirm" to attest they had stored it safely. It could not have held the
  // phrase: register did not run until the step AFTER this one.
  test.setTimeout(240_000)
  await bootWizard(page, 'dark')

  for (const step of STEP_IDS) {
    if (step === 'recoverykit') break
    await settle(page)
    await advance(page, step)
  }
  await settle(page)

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: /download recovery kit/i }).first().click(),
  ])
  const path = await download.path()
  expect(path, 'the recovery kit download produced no file').toBeTruthy()
  const { readFileSync } = await import('node:fs')
  const payload: unknown = JSON.parse(readFileSync(path!, 'utf8'))

  const rec = (x: unknown): Record<string, unknown> => (typeof x === 'object' && x !== null ? x as Record<string, unknown> : {})
  const kit = rec(rec(payload).kit)
  expect(
    kit.master_recovery_phrase,
    'the recovery kit does not contain the recovery phrase, so it recovers nothing',
  ).toBe(RECOVERY_PHRASE)
  expect(kit.ulid).toBe('01J8ZC4K7QF2VN9YB3RXPD6MTA')
})

test('the confirm gate will not accept an attestation for a kit that was never downloaded', async ({ page }) => {
  test.setTimeout(240_000)
  await bootWizard(page, 'dark')
  for (const step of STEP_IDS) {
    if (step === 'recoverykit') break
    await settle(page)
    await advance(page, step)
  }
  await settle(page)

  // Signing "I have saved my recovery kit" for a file you never asked for is a
  // signature on nothing, so the field is inert until the download happens.
  await expect(page.locator('#wz-kit-confirm')).toBeDisabled()
  await expect(page.getByRole('button', { name: /finish setup/i })).toBeDisabled()
})

test('SSH can be skipped — it is not a condition of owning the machine', async ({ page }) => {
  // The step had no way past it without generating a keypair and ticking "I
  // have saved this private key in a secure location", so a user who does not
  // want a shell on their box had to make a key and attest to storing it.
  test.setTimeout(240_000)
  await bootWizard(page, 'dark')
  for (const step of STEP_IDS) {
    if (step === 'ssh') break
    await settle(page)
    await advance(page, step)
  }
  await settle(page)

  await expect(page.getByText(/Remote access over SSH/i)).toBeVisible()
  await page.getByRole('button', { name: /skip — no ssh/i }).click()
  await settle(page)
  await expect(page.getByText(/Your recovery kit/i)).toBeVisible()
})

test('a box that cannot record setup-complete says so instead of pretending', async ({ page }) => {
  // GET /api/setup/status is os.Stat on /var/lib/vulos/.setup-complete, and the
  // only writer of that file in the whole product is a `touch` sent through
  // POST /api/exec — which is admin-gated and kill-switchable
  // (VULOS_DISABLE_EXEC -> 503). When it fails, nothing else ever marks setup
  // done and the wizard runs again on the next boot, with the account already
  // created so register then fails on a duplicate username. The user should
  // hear about that at the time, not discover it after a reboot.
  test.setTimeout(240_000)
  await bootWizard(page, 'dark', {
    'POST /api/exec': json({ error: 'exec endpoint disabled by configuration' }, 503),
  })

  await walkWizard(page, async () => {})
  await page.getByRole('button', { name: /enter vulos/i }).first().click()
  // The Ready step routes through the optional private-AI offer first.
  await settle(page)
  const skip = page.getByRole('button', { name: /skip for now/i }).first()
  if (await skip.isVisible().catch(() => false)) await skip.click()
  await settle(page)

  await expect(
    page.getByText(/could not record that it is done/i),
    'the wizard silently swallowed a failure to mark setup complete',
  ).toBeVisible({ timeout: 20_000 })
})
