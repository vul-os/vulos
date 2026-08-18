/**
 * settings-honesty.e2e.ts — what Settings says when the box has not told it.
 *
 * The Settings audit (roadmap/SETTINGS-AUDIT.md) found that the WRITES in
 * core/Settings.tsx were in good shape — two previous passes built apiGet,
 * apiSend and useApiError precisely to fix them — and that eleven of sixteen
 * remaining defects were on the READING side: a value the box never sent, drawn
 * as a confident fact.
 *
 * The mechanism is worth stating because it is not obvious and it recurs. Every
 * `/api` response in that file goes through a `toX()` narrower. Handed a 5xx
 * JSON error body — or a 200 with a field missing — the narrower passes
 * `isRecord`, keeps none of the fields it was asked for, and returns a
 * well-formed record of `undefined`s that is **not null**. Every `{data && …}`
 * gate therefore opens, and every `x ? 'A' : 'B'` inside falls to 'B'. The
 * panel renders a designed, plausible, wrong answer with nothing to mark it as
 * a failure.
 *
 * These specs pin the four worst instances by driving the box's answers. Note
 * that the mock backend answers any unmocked /api route with an empty 200 —
 * which is exactly the "answered, but told you nothing" condition — so several
 * of these were reachable in the existing suite and rendered green.
 *
 * Each assertion is negative on purpose: the point is not that a spinner
 * appears, it is that a specific false sentence does NOT.
 */
import { test, expect } from '@playwright/test'
import { openSettings, gotoSection } from './settings-harness.js'
import { json } from './mock-backend.js'

test.describe('Settings does not assert what the box never said', () => {
  /**
   * The finding this exists for: "Current mode" was a two-state ternary over a
   * three-state selector, so `local-fs` — the DEFAULT, every byte on this box's
   * own disk — fell into the else branch and reported "Central Tigris
   * (default)": hosted, third-party, S3.
   */
  test('a box on the default local-fs storage mode is not told its bytes are on hosted Tigris', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/storagemode': json({ mode: 'local-fs' }),
    })
    expect(await gotoSection(page, 'Storage Mode')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    await expect(panel).toContainText('This device (default)')
    // The whole point. This box is not on Tigris and must not be told it is.
    await expect(panel).not.toContainText('Central Tigris (default)')
  })

  test('a storage mode the box could not report reads as unknown, not as hosted', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/storagemode': json({ error: 'storagemode store unavailable' }, 500),
    })
    expect(await gotoSection(page, 'Storage Mode')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    // A failed read must read as a failed read...
    await expect(panel).toContainText('Could not read the storage mode')
    // ...and must not name a mode. Either concrete label here would be invented.
    await expect(panel).not.toContainText('Central Tigris (default)')
    await expect(panel).not.toContainText('This device (default)')
  })

  /**
   * About's Graphics card was gated on `sys` being non-null rather than on the
   * box having reported any graphics at all, and every row inside was a
   * two-branch ternary. An empty answer produced three measurements nobody
   * took.
   */
  test('About states no hardware findings for a box that reported no hardware', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/system/info': json({ error: 'system info unavailable' }, 500),
    })
    expect(await gotoSection(page, 'About')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    await expect(panel).toContainText('Vulos OS')    // the pane did render
    await expect(panel).not.toContainText('Tier 0')  // no GPU tier was measured
    await expect(panel).not.toContainText('X11 SHM') // no capture path was measured

    // The OS row, scoped. A pane-wide `not.toContainText('Debian Linux')` was
    // the first thing written here and it failed on CORRECT code: the string
    // also appears twice as honest static text on this pane — the Backend row
    // reads "Go + Debian Linux" and the footer reads "Powered by Debian Linux",
    // neither of which is a measurement of anything. The assertion was
    // measuring branding. What the defect was about is the OS row, which used
    // to fall back to the bare claim "Debian Linux" when the box had reported
    // no version at all; that is the row this pins.
    const osRow = panel.locator('div.grid').filter({ hasText: /^OS/ })
    await expect(osRow).toHaveCount(1) // a row that resolves to nothing would satisfy the next line vacuously
    await expect(osRow).not.toContainText('Debian')
  })

  /**
   * `has_pin !== false` treated an absent field as "a PIN is set", so the panel
   * announced a protected device and offered to remove a credential the box had
   * never mentioned. `attempts_left ?? 5` invented the lockout budget beside it.
   */
  test('Device PIN neither claims a PIN nor offers to remove one the box did not report', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/auth/pin/status': json({}),
    })
    expect(await gotoSection(page, 'Device PIN')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    // Both assertions below are satisfied by a panel that rendered NOTHING, and
    // this is the only spec in the file with no positive assertion of its own —
    // the others each anchor on a string their section owns. `gotoSection`
    // returning true does not close the gap: it only means a nav button existed
    // and was clicked. So anchor on the section's own label first; if the
    // Device PIN panel is not on screen, this fails instead of passing blind.
    await expect(panel).toContainText('PIN status')
    await expect(panel).not.toContainText('5 attempts remaining')
    // A destructive control for a credential whose existence is unknown.
    await expect(page.getByRole('button', { name: 'Remove PIN' })).toHaveCount(0)
  })

  /**
   * `updateProfile` returns 'ok' | 'rejected' | 'unreachable' and NEVER throws,
   * so the try/catch that used to guard this button was dead code and every
   * failure fell through to `setSaved(true)`.
   */
  test('Account does not report Saved when the box refuses the write', async ({ page }) => {
    await openSettings(page, 'dark', {
      'PUT /api/profiles/u1': json({ error: 'not permitted' }, 403),
    })
    expect(await gotoSection(page, 'Account')).toBe(true)

    await page.getByLabel('Display name').fill('Grace Hopper')
    await page.getByRole('button', { name: 'Save' }).click()

    const panel = page.locator('[class*="@container/win"]')
    await expect(panel).toContainText('refused')
    // The button must not have claimed success for a 403.
    await expect(page.getByRole('button', { name: 'Saved' })).toHaveCount(0)
  })

  /**
   * The mode map read in the other direction: a box that IS on hosted Tigris
   * must be told so. Without this, "never say Tigris" would be satisfiable by a
   * panel that can never say Tigris at all, which is a different lie.
   */
  test('a box that IS on hosted Tigris is told so', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/storagemode': json({ mode: 'central-tigris' }),
    })
    expect(await gotoSection(page, 'Storage Mode')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    await expect(panel).toContainText('Central Tigris (hosted)')
  })

  /**
   * The control for this file. Every assertion above is a `not.toContainText`,
   * and a locator that matches nothing satisfies all of them — so a typo in the
   * panel selector, or a Settings that failed to open, would report the whole
   * suite green. This proves the selector resolves and the sweep can see text
   * through it.
   *
   * It asserts ONLY on static chrome, and that is the point. This control used
   * to assert "Central Tigris (hosted)" — a value produced by the same storage
   * label map that the first spec in this file pins. So a single mutation of
   * that map took out the spec AND the control together, and the run then
   * proved nothing in either direction: a red control means "the instrument is
   * broken", which invalidates every other result in the same run.
   *
   * That is not hypothetical. It is exactly what happened on 2026-08-17, and it
   * cost a later agent a full session to establish that the 5-failure log it
   * inherited had been a legitimate mutation run rather than a red gate. A
   * control must be independent of every subject it certifies, so this one
   * reads one string no box can influence — Settings' own subtitle. Nothing the
   * box says, and no mutation of any honesty subject in this file, can change
   * it, so if this test is red the instrument really is broken.
   *
   * Verified both ways: it is green on clean HEAD, it stays green while all
   * five honesty specs fail under re-planted defects, and it goes red when the
   * subtitle itself is changed.
   */
  test('CONTROL — the panel locator resolves and the assertions can fail', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/storagemode': json({ mode: 'central-tigris' }),
    })
    expect(await gotoSection(page, 'Storage Mode')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    await expect(panel).toHaveCount(1)
    // Settings' own subtitle, and deliberately nothing else. An earlier draft
    // of this control also asserted a sentence from the Storage Mode section's
    // prose, to prove the requested SECTION had rendered and not just the shell
    // around it. That is a real thing to check, but it re-created the coupling
    // this control was just rewritten to remove: that prose is S1 in the audit,
    // it has been edited once already for being wrong, and editing it again
    // would turn this control red for a reason that has nothing to do with the
    // instrument. Proving its own section rendered is each spec's own job, via
    // the positive anchor every one of them now carries.
    await expect(panel).toContainText('Configure your device')
  })
})
