import { expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

/**
 * Drive the first-boot setup wizard, step by step, the way a user does.
 *
 * This exists because the wizard had NO end-to-end coverage of any kind until
 * 2026-08-15, and its fifteen steps had never been executed on a shipped box —
 * build.sh pre-created /var/lib/vulos/.setup-complete, so every image since
 * 2026-03-31 booted claiming setup was already finished.
 * first-boot-setup.e2e.ts proves the wizard APPEARS. Nothing proved any step
 * past the first one renders, advances, or sends anything, and walking it by
 * hand found that four of them sent nothing at all.
 *
 * Every spec that needs the wizard walks it through THIS module, so a renamed
 * control breaks one walker rather than drifting silently past a copy in each
 * spec. STEP_IDS is asserted against Setup.tsx's own exported STEPS by
 * onboarding-flow.e2e.ts, so this list and the real wizard cannot diverge.
 */

/** The wizard's step ids, in order, for the default ("new system") flow. */
export const STEP_IDS = [
  'welcome',
  'chooser',
  'device',
  'language',
  'timezone',
  'network',
  'account',
  'pin',
  'apps',
  'appearance',
  'identity',
  'storage',
  'ssh',
  'recoverykit',
  'ready',
] as const

export type StepId = (typeof STEP_IDS)[number]

export const RECOVERY_PHRASE =
  'ridge acid lumber velvet tunnel oyster prism candle harbor mimic fossil sprout ' +
  'anchor kettle marble opaque ribbon thicket vulture jasmine quartz drift ember nomad'

/**
 * A box that has not been set up, with every endpoint healthy.
 *
 * The session-gated endpoints answer 200 here so a walk exercises the SUCCESS
 * path of every step. What a real PRE-ACCOUNT first boot got from them — 401,
 * because none of them are in the backend's publicPaths — is a separate test in
 * onboarding-flow.e2e.ts. Conflating the two would hide whichever one broke.
 */
export const FIRST_BOOT: Record<string, unknown> = {
  'GET /api/setup/status': json({ setup_complete: false }),
  'GET /api/setup/mode': json({ mode: 'setup' }),
  'GET /api/auth/status': json({ has_users: false }),
  'GET /api/auth/me': json({}, 401),
  'GET /api/device-profile': json({ suggested: 'pc' }),
  'GET /api/wifi/scan': json([
    { bssid: 'a1', ssid: 'Home-5G', signal: -42, band: '5GHz', security: 'WPA2' },
    { bssid: 'a2', ssid: 'Neighbour', signal: -67, band: '2.4GHz', security: 'WPA2' },
  ]),
  'GET /api/identity': json({ ulid: '01J8ZC4K7QF2VN9YB3RXPD6MTA', hostname: 'vulos-box' }),
  'POST /api/identity/hostname': json({ ok: true }),
  'POST /api/setup/storage': json({ ok: true }),
  'PUT /api/storagemode': json({ ok: true }),
  'POST /api/ssh/authorized': json({ ok: true }),
  'POST /api/setup/apps': json({ ok: true }),
  'POST /api/auth/pin/set': json({ ok: true }),
  'POST /api/exec': json({ exitCode: 0, output: 'done' }),
  'POST /api/auth/register': json({ ok: true, master_recovery_phrase: RECOVERY_PHRASE }),
}

/**
 * Boot the shell on a machine reporting setup_complete=false, in an EXPLICIT
 * theme with animation frozen.
 *
 * Both details are load-bearing and both were learned the hard way by
 * auth-contrast.e2e.ts: left to resolve on its own this suite booted light, so
 * a "both themes" gate measured one theme twice; and a scan landing mid-fade
 * reads partly-transparent text over a partly-drawn background, a number
 * belonging to no state a user ever sees.
 */
export async function bootWizard(
  page: Page,
  theme: 'dark' | 'light',
  overrides: Record<string, unknown> = {},
): Promise<void> {
  await page.addInitScript((t) => {
    try {
      localStorage.setItem('vulos-theme', t)
    } catch {
      /* storage blocked — the attribute below is what actually decides */
    }
    document.documentElement.setAttribute('data-theme', t)
  }, theme)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await installBackend(page, { ...FIRST_BOOT, ...overrides })
  await page.goto('/')

  await expect(
    page.getByRole('button', { name: /get started/i }),
    'the setup wizard did not render on a box reporting setup_complete=false',
  ).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
}

/** Wait for the step transition and any entrance animation to settle. */
export async function settle(page: Page): Promise<void> {
  await page.waitForTimeout(420)
  await page
    .waitForFunction(() => document.getAnimations().every((a) => a.playState === 'finished'), null, {
      timeout: 10_000,
    })
    .catch(() => {
      /* a looping decorative animation never "finishes"; reducedMotion is the real guard */
    })
}

/**
 * The recovery-phrase reveal, which the ACCOUNT step now shows inline after it
 * registers. Registration used to happen on the last step, which is what made
 * the four steps in between post into a 401 — see AccountStep's docstring.
 */
export async function passMasterKeyReveal(page: Page): Promise<void> {
  await expect(page.getByText(/save your recovery phrase/i)).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: /tap to reveal/i }).click()
  await settle(page)
  await page.locator('input[type=checkbox]').first().check({ force: true })
  await settle(page)
  await page.getByRole('button', { name: /continue/i }).first().click()
}

/**
 * Fill in whatever the CURRENT step needs and move to the next one.
 * Returns without advancing for 'ready', the terminal step.
 */
export async function advance(page: Page, step: StepId): Promise<void> {
  const next = (rx: RegExp) => page.getByRole('button', { name: rx }).first().click()

  switch (step) {
    case 'welcome':
      await next(/get started/i)
      break
    case 'chooser':
      await next(/new system/i)
      break
    case 'device':
      await page.getByRole('button', { name: /10-foot UI/i }).first().click()
      await settle(page)
      await next(/continue/i)
      break
    case 'language':
      await page.getByRole('button', { name: /English/i }).first().click()
      await settle(page)
      await next(/continue/i)
      break
    case 'timezone':
      await page.locator('#wz-tz-search').fill('Johannesburg')
      await settle(page)
      await page.getByRole('button', { name: /Johannesburg/ }).first().click()
      await settle(page)
      await next(/continue/i)
      break
    case 'network':
      await next(/scan/i)
      await settle(page)
      await page.getByRole('button', { name: /Home-5G/ }).first().click()
      await settle(page)
      await page.locator('#wz-wifi-pw').fill('hunter22')
      await next(/continue/i)
      break
    case 'account':
      await page.locator('#wz-name').fill('Ada Lovelace')
      await page.locator('#wz-username').fill('ada')
      await page.locator('#wz-password').fill('correct-horse-battery')
      await page.locator('#wz-password2').fill('correct-horse-battery')
      await settle(page)
      await next(/create account/i)
      await settle(page)
      await passMasterKeyReveal(page)
      break
    case 'pin':
      await page.locator('#wz-pin').fill('1234')
      await settle(page)
      await page.locator('#wz-pin-confirm').fill('1234')
      await settle(page)
      await next(/set pin/i)
      break
    case 'apps':
    case 'appearance':
    case 'identity':
    case 'storage':
      await next(/continue/i)
      break
    case 'ssh':
      await next(/generate an ed25519 keypair/i)
      await expect(page.getByText(/^SHA256:/)).toBeVisible({ timeout: 20_000 })
      await page.locator('input[type=checkbox]').first().check({ force: true })
      await settle(page)
      await next(/continue/i)
      break
    case 'recoverykit':
      await next(/download recovery kit/i)
      await settle(page)
      await page.locator('#wz-kit-confirm').fill('confirm')
      await settle(page)
      await next(/finish setup/i)
      break
    case 'ready':
      return
  }
  await settle(page)
}

/**
 * Walk every step in order, calling `onStep` once the step has rendered and
 * settled. The callback runs BEFORE the step is filled in, so it sees the state
 * a user first meets.
 */
export async function walkWizard(
  page: Page,
  onStep: (step: StepId) => Promise<void>,
): Promise<void> {
  for (const step of STEP_IDS) {
    await settle(page)
    await onStep(step)
    await advance(page, step)
  }
}
