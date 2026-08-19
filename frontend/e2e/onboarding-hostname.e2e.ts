import { test, expect, type Page, type Route } from '@playwright/test'
import { bootWizard, advance, settle, STEP_IDS } from './onboarding-walk.js'

/**
 * INIT-05, the hostname availability probe: the wizard asking the box whether
 * the name being typed is already claimed on this LAN.
 *
 * NOTHING under e2e/ referenced IS05 or /api/identity/hostname/available. The
 * step has unit tests, but this is a control that TALKS TO THE BOX and renders
 * four different things depending on what comes back, and no browser-level test
 * had ever driven it. The unit tests mock `fetch` on a component rendered in
 * isolation; they cannot see the step reached through ten preceding wizard
 * steps, in the real bundle, with the real debounce running on real timers.
 *
 * What is being protected, in the wizard's own words: a collision is surfaced
 * WHILE THE OWNER TYPES, because if two boxes both claim a name, avahi silently
 * renames the loser to vulos-2 hours later — a name that is in no certificate,
 * so that box then fails TLS with nothing connecting it to anything the owner
 * did.
 *
 * The state that matters most here is the one where the box does not answer.
 * The step must then say NOTHING: a green "this name is free" on the strength
 * of a request that failed is this codebase's signature defect — a failure
 * drawn as a designed state — and it would be reporting the absence of a
 * collision it never checked for.
 */

const AVAILABLE_PATH = '/api/identity/hostname/available'

/** Walk the wizard as far as the identity step and stop, without filling it. */
async function walkToIdentity(page: Page): Promise<void> {
  for (const step of STEP_IDS) {
    if (step === 'identity') break
    await settle(page)
    await advance(page, step)
  }
  await settle(page)
  await expect(
    page.locator('#wz-hostname'),
    'the walk did not arrive at the identity step',
  ).toBeVisible({ timeout: 20_000 })
}

/**
 * Answer the availability probe with `handler`, and record the names it was
 * asked about. Installed on top of the wizard's own backend, so it wins for
 * this one path only.
 */
async function interceptProbe(
  page: Page,
  handler: (name: string, route: Route) => Promise<void> | void,
): Promise<string[]> {
  const asked: string[] = []
  await page.route(`**${AVAILABLE_PATH}*`, async (route) => {
    const name = new URL(route.request().url()).searchParams.get('name') ?? ''
    asked.push(name)
    await handler(name, route)
  })
  return asked
}

function jsonRoute(body: unknown, status = 200) {
  return (_name: string, route: Route) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
}

test.describe('INIT-05 — is this name already taken on the network?', () => {
  test('says the name is free only once the box has actually said so', async ({ page }) => {
    test.setTimeout(240_000)
    await bootWizard(page, 'dark')
    const asked = await interceptProbe(page, jsonRoute({ available: true }))
    await walkToIdentity(page)

    // Nothing typed yet: the step has no verdict to offer and must not imply one.
    await expect(page.getByText(/^This name is free/i)).toHaveCount(0)
    await expect(page.getByText(/^Checking whether/i)).toHaveCount(0)

    await page.locator('#wz-hostname').fill('study')

    await expect(page.getByText(/^This name is free/i)).toBeVisible({ timeout: 15_000 })
    // The verdict has to be about the name on screen, so the box must have been
    // asked about that exact name — not about a prefix left by the debounce.
    expect(asked, 'the box was never asked about the typed name').toContain('study')
  })

  test('warns, while the owner is still typing, that another box answers to that name', async ({ page }) => {
    test.setTimeout(240_000)
    await bootWizard(page, 'dark')
    await interceptProbe(page, jsonRoute({ available: false, taken_by: '192.168.1.9' }))
    await walkToIdentity(page)

    await page.locator('#wz-hostname').fill('study')

    const alert = page.getByRole('alert')
    await expect(alert, 'a collision was not surfaced while typing').toBeVisible({ timeout: 15_000 })
    await expect(alert).toContainText(/already answers to that name/i)
    // Which box, so the owner can go and find it.
    await expect(alert).toContainText('192.168.1.9')
    // And it must not simultaneously claim the name is free.
    await expect(page.getByText(/^This name is free/i)).toHaveCount(0)
  })

  test('reports that it is checking from the keystroke, not from the reply', async ({ page }) => {
    test.setTimeout(240_000)
    await bootWizard(page, 'dark')

    // Held open, so the in-flight state is observable rather than a race.
    let release: () => void = () => {}
    const held = new Promise<void>((r) => { release = r })
    await interceptProbe(page, async (_name, route) => {
      await held
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ available: true }),
      })
    })
    await walkToIdentity(page)

    await page.locator('#wz-hostname').fill('study')

    await expect(
      page.getByText(/^Checking whether/i),
      'the step did not say it was checking while the probe was in flight',
    ).toBeVisible({ timeout: 15_000 })
    // It must not have decided anything yet.
    await expect(page.getByText(/^This name is free/i)).toHaveCount(0)
    await expect(page.getByRole('alert')).toHaveCount(0)

    release()
    await expect(page.getByText(/^This name is free/i)).toBeVisible({ timeout: 15_000 })
    await expect(page.getByText(/^Checking whether/i)).toHaveCount(0)
  })

  // The honesty case, and the reason this file exists.
  test('says nothing at all when the box could not answer the check', async ({ page }) => {
    test.setTimeout(240_000)
    await bootWizard(page, 'dark')
    const asked = await interceptProbe(page, jsonRoute({ error: 'unavailable' }, 500))
    await walkToIdentity(page)

    await page.locator('#wz-hostname').fill('study')

    // Give the debounce and the failing request time to complete, so this is
    // "it finished and stayed quiet" rather than "we looked too early".
    await expect.poll(() => asked.length, { timeout: 15_000 }).toBeGreaterThan(0)
    await page.waitForTimeout(1_500)

    // A tick here would assert the absence of a collision that was never
    // checked for, and the owner would ship a colliding name believing it was
    // cleared.
    await expect(
      page.getByText(/^This name is free/i),
      'the step claimed the name was free after the check FAILED',
    ).toHaveCount(0)
    await expect(
      page.getByRole('alert'),
      'the step claimed the name was taken after the check FAILED',
    ).toHaveCount(0)
    // And it must not be stuck saying it is still checking, either — the probe
    // finished, it just could not answer.
    await expect(
      page.getByText(/^Checking whether/i),
      'the step was still reporting an in-flight check after the probe had failed',
    ).toHaveCount(0)

    // The step must still be usable: a check that could not run is not a reason
    // to block the owner from naming their box.
    await expect(page.getByRole('button', { name: /continue/i }).first()).toBeEnabled()
  })

  // The SECOND probe is held open for the whole assertion, and that is what
  // makes this test able to fail.
  //
  // Written the obvious way — retype, then assert the warning is gone — it
  // proves nothing. Playwright retries toHaveCount(0) until it succeeds, and
  // the replacement probe answers "free" about 400ms later, so the warning
  // disappears on its own and the assertion passes even when the verdict is NOT
  // tied to the name it was measured for. Verified: deleting that guard from
  // Setup.tsx left the first version of this test green.
  //
  // Holding the reply removes the other exit. While the box has said nothing
  // about 'studio', the only thing that can clear a warning about 'study' is
  // the step knowing the verdict does not belong to the name on screen — so the
  // assertion is on the CHECKING state being shown, which is a positive claim
  // about what the step believes rather than the absence of something.
  test('drops a verdict the moment the name it was measured for changes', async ({ page }) => {
    test.setTimeout(240_000)
    await bootWizard(page, 'dark')

    let releaseSecond: () => void = () => {}
    const secondHeld = new Promise<void>((r) => { releaseSecond = r })
    await interceptProbe(page, async (name, route) => {
      if (name === 'study') {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ available: false, taken_by: '192.168.1.9' }),
        })
        return
      }
      // Every other name: the box has not answered yet, and does not until the
      // assertions below have run.
      await secondHeld
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ available: true }),
      })
    })
    await walkToIdentity(page)

    await page.locator('#wz-hostname').fill('study')
    await expect(page.getByRole('alert')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByRole('alert')).toContainText(/already answers to that name/i)

    await page.locator('#wz-hostname').fill('studio')

    // Nothing is known about 'studio' yet, and the step must say exactly that.
    await expect(
      page.getByText(/^Checking whether/i),
      'after retyping, the step did not go back to "checking" — it is still showing a verdict measured for a different name',
    ).toBeVisible({ timeout: 15_000 })
    // A certainty about one name must never be displayed against another.
    await expect(
      page.getByRole('alert'),
      'the collision warning for "study" was still shown against "studio"',
    ).toHaveCount(0)

    releaseSecond()
    await expect(page.getByText(/^This name is free/i)).toBeVisible({ timeout: 15_000 })
  })
})
