import { test, expect } from '@playwright/test'
import { belowAA, textNodeCount } from './contrast-scan.js'
import { bootWizard, walkWizard, STEP_IDS, type StepId } from './onboarding-walk.js'

/**
 * Every step of first-boot setup must meet WCAG AA, on both themes.
 *
 * NOTHING measured this. auth-contrast.e2e.ts covers the two screens AFTER
 * setup (sign-in and create-account); shell-contrast and app-contrast boot an
 * authenticated shell. The fifteen-step wizard — the most important screen a
 * new user ever sees, and the one nobody had ever run, because build.sh
 * pre-created the setup-complete marker in every image shipped since March —
 * was scanned by no gate at all.
 *
 * What that cost, measured on composited pixels the first time anyone looked.
 * EVERY step carried sub-AA text, in BOTH themes:
 *
 *   "5 GB" / "100 GB"             1.92 dark  1.48 light
 *   recovery-QR payload string    1.91 dark  1.42 light
 *   "Set PIN", white on amber     1.89 dark  3.19 light
 *   step counter "/ 15"           2.11 dark  2.18 light   (on all 15 steps)
 *   "Shared encrypted storage…"   2.43 dark
 *   "← Back"                      2.55 dark
 *   SSH fingerprint                          3.10 light
 *
 * …and roughly forty more between 3.6 and 4.4.
 *
 * The cause was not one bad value. The wizard styled itself in ~200 hardcoded
 * `neutral-*` classes — a lightness ladder, not a semantic one — and used its
 * bottom rungs for real copy, so unreadable text was exactly as easy to write
 * as readable text. src/auth/setup.css replaced them with three text tiers that
 * cannot fail. This spec is what stops the ladder coming back.
 *
 * Measured on COMPOSITED PIXELS via contrast-scan.ts, not on tokens, because
 * the failure is never a value in a file — it is a pair, and `opacity` is half
 * of it, invisible to any stylesheet audit.
 */

/**
 * Text-node floors, per step, one below each step's real measured count.
 *
 * Per STEP for the same reason auth-contrast.e2e.ts made its floors per BRANCH:
 * one shared floor has to be set to the sparsest screen (welcome, 4 nodes),
 * which leaves it far too slack to notice a gutted recovery-kit step (20). A
 * step that renders nothing must not pass this gate vacuously — belowAA([]) is
 * trivially satisfied by a blank page, and a wizard that crashed on step 9
 * would otherwise report ten clean steps.
 *
 * These are MEASURED, not guessed. Raising one to make a run pass is the wrong
 * move in every case: if a step legitimately loses content, say so in the
 * commit the way 27d607d7 did.
 */
const MIN_TEXT_NODES: Record<StepId, number> = {
  welcome: 3,
  chooser: 6,
  device: 12,
  language: 40,
  timezone: 5,
  network: 5,
  account: 6,
  pin: 4,
  apps: 7,
  appearance: 13,
  identity: 8,
  storage: 8,
  ssh: 4,
  recoverykit: 17,
  ready: 30,
}

for (const theme of ['dark', 'light'] as const) {
  test(`every first-boot setup step meets WCAG AA on the ${theme} theme`, async ({ page }) => {
    // Fifteen steps, each with a settle and an Ed25519 keygen in the middle.
    test.setTimeout(240_000)

    await bootWizard(page, theme)

    const failures: string[] = []
    const thin: string[] = []

    await walkWizard(page, async (step) => {
      const measured = await textNodeCount(page)
      if (measured <= MIN_TEXT_NODES[step]) {
        thin.push(`${step}: rendered ${measured} text nodes, floor is >${MIN_TEXT_NODES[step]}`)
      }
      for (const f of await belowAA(page)) {
        failures.push(`[${step}] ${f}`)
      }
    })

    // Vacuity first: a step that rendered nothing would otherwise pass the
    // contrast assertion silently, and the reader of a green run would learn
    // the opposite of the truth.
    expect(thin, 'a setup step rendered almost no text, so its contrast check would pass vacuously').toEqual([])

    expect(
      failures,
      `first-boot setup text below WCAG AA on ${theme}, measured on composited pixels`,
    ).toEqual([])
  })
}

test('the walker visits every step the wizard actually defines', async ({ page }) => {
  // Guards the gate itself. If a step is added to Setup.tsx and not to
  // STEP_IDS, the two loops above simply never visit it and report a clean
  // fifteen — a coverage assertion, in the same spirit as the one in
  // TestBuildDoesNotPreCompleteSetup, so this file fails loudly rather than
  // quietly measuring less than it claims.
  test.setTimeout(240_000)
  await bootWizard(page, 'dark')

  const visited: StepId[] = []
  await walkWizard(page, async (step) => { visited.push(step) })

  expect(visited).toEqual([...STEP_IDS])
  expect(visited.length, 'the wizard is documented everywhere as fifteen steps').toBe(15)
})
