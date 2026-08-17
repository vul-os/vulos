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
    await expect(panel).toContainText('Vulos OS')          // the pane did render
    await expect(panel).not.toContainText('Tier 0')        // no GPU tier was measured
    await expect(panel).not.toContainText('X11 SHM')       // no capture path was measured
    await expect(panel).not.toContainText('Debian Linux')  // no OS version was reported
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
   * The control for this file. Every assertion above is a `not.toContainText`,
   * and a locator that matches nothing satisfies all of them — so a typo in the
   * panel selector, or a Settings that failed to open, would report the whole
   * suite green. This proves the selector resolves and the sweep can see text
   * through it.
   */
  test('CONTROL — the panel locator resolves and the assertions can fail', async ({ page }) => {
    await openSettings(page, 'dark', {
      'GET /api/storagemode': json({ mode: 'central-tigris' }),
    })
    expect(await gotoSection(page, 'Storage Mode')).toBe(true)

    const panel = page.locator('[class*="@container/win"]')
    await expect(panel).toHaveCount(1)
    // A box that IS on hosted Tigris is told so — the same locator and the same
    // kind of assertion that the specs above use negatively.
    await expect(panel).toContainText('Central Tigris (hosted)')
  })
})
