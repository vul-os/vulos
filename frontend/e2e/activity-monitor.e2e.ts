import { test, expect } from '@playwright/test'
import { bootActivity, APP, PROCESSES } from './activity-harness'

/**
 * Behaviour of the Activity Monitor against a real render.
 *
 * A parallel agent found six defects tonight that a green unit suite, eslint
 * and tsc all missed — including a rail rendering 350px off the bottom of the
 * screen. Its own viewport test had passed because it checked left and right
 * while the rail grew DOWN. So these assert against composited geometry and
 * write screenshots that were opened and looked at, not just captured.
 */

const SHOTS = 'e2e-shots/activity'

test.describe('Activity Monitor', () => {
  test('lists processes with per-process user, CPU, memory and threads', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)

    const app = page.locator(APP)
    await expect(app.getByRole('button', { name: /^Processes / })).toBeVisible()

    // The fixture's own rows, not a count: a count passes on any eight rows.
    for (const name of ['vulos-server', 'postgres', 'gimp', 'kthreadd']) {
      await expect(app.getByText(name, { exact: true }).first()).toBeVisible()
    }
    // The columns the founder asked for, populated rather than merely present.
    await expect(app.getByText('98.6', { exact: true })).toBeVisible()   // gimp CPU
    await expect(app.getByText('774.4 MB', { exact: true })).toBeVisible() // gimp RSS
    await expect(app.getByText('postgres', { exact: true }).first()).toBeVisible()

    await page.screenshot({ path: `${SHOTS}/processes-dark.png` })
  })

  test('sorts by a clicked column, and reverses on a second click', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const app = page.locator(APP)

    // Default sort is CPU descending, so gimp (98.6) leads.
    const pidCells = () => app.locator('[role="row"] > span:first-child')
    await expect(pidCells().first()).toHaveText('2277')

    await app.getByRole('button', { name: 'Sort by PID' }).click()
    await expect(pidCells().first()).toHaveText('3400') // descending
    await app.getByRole('button', { name: 'Sort by PID' }).click()
    await expect(pidCells().first()).toHaveText('1')    // ascending
  })

  test('filters rows by the search box', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const app = page.locator(APP)

    const rows = app.locator('[role="row"]')
    await expect(rows).toHaveCount(PROCESSES.length)
    await app.getByLabel('Filter rows').fill('postgres')
    await expect(rows).toHaveCount(1)
    await expect(app.getByText('1044', { exact: true })).toBeVisible()
  })

  // The protect list is enforced on the server, but a UI that OFFERS the
  // action and then 403s teaches the user the feature is unreliable rather
  // than that the process is off limits.
  test('cannot offer to end a protected process, and says why', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const app = page.locator(APP)

    await app.getByText('init', { exact: true }).first().click()
    // exact, because Playwright's accessible-name matching is substring +
    // case-insensitive by default, so 'Quit' also matches 'Force Quit'.
    await expect(app.getByRole('button', { name: 'Quit', exact: true })).toBeDisabled()
    await expect(app.getByRole('button', { name: 'Force Quit' })).toBeDisabled()
    await expect(app.getByText(/pid 1 is the system init/)).toBeVisible()

    await page.screenshot({ path: `${SHOTS}/protected-row-dark.png` })
  })

  // The whole recycled-pid defence rests on the client echoing back the start
  // time it saw. If the request ever goes out without one, the server is being
  // asked to signal whatever now holds that number.
  test('sends the observed start time with a force quit', async ({ page }) => {
    test.setTimeout(120_000)
    const posted: Record<string, unknown>[] = []
    await bootActivity(page, {
      overrides: {
        'POST /api/system/processes/signal': (req: { postDataJSON: () => Record<string, unknown> }) => {
          posted.push(req.postDataJSON())
          return { status: 200, contentType: 'application/json', body: JSON.stringify({ pid: 2277, outcome: 'killed', signals: ['SIGKILL'] }) }
        },
      },
    })
    const app = page.locator(APP)

    await app.getByText('gimp', { exact: true }).first().click()
    await app.getByRole('button', { name: 'Force Quit' }).click()

    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText(/will not get a chance to save/)).toBeVisible()
    await page.screenshot({ path: `${SHOTS}/force-quit-confirm-dark.png` })
    await dialog.getByRole('button', { name: 'Force quit' }).click()

    await expect(app.getByRole('status')).toContainText(/killed/)
    expect(posted).toHaveLength(1)
    expect(posted[0]).toMatchObject({ pid: 2277, mode: 'force' })
    expect(typeof posted[0].start).toBe('number')
    expect(posted[0].start).toBe(88410)
  })

  // "survived" means the signal was delivered and the process is still there.
  // Rendering that as success would leave the user believing they fixed a
  // stuck disk.
  test('reports an unkillable D-state task as a failure, with the reason', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page, {
      overrides: {
        'POST /api/system/processes/signal': {
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ pid: 2301, outcome: 'survived', state: 'D', signals: ['SIGKILL'] }),
        },
      },
    })
    const app = page.locator(APP)

    await app.getByText('cp', { exact: true }).first().click()
    await app.getByRole('button', { name: 'Force Quit' }).click()
    await page.getByRole('dialog').getByRole('button', { name: 'Force quit' }).click()

    const status = app.getByRole('status')
    await expect(status).toContainText(/could not be ended/)
    await expect(status).toContainText(/storage/)
    await page.screenshot({ path: `${SHOTS}/survived-notice-dark.png` })
  })

  // A stale pid must not be presented as "already gone", which would invite a
  // retry against whatever now holds the number.
  test('surfaces an identity mismatch rather than silently doing nothing', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page, {
      overrides: {
        'POST /api/system/processes/signal': {
          status: 409, contentType: 'application/json',
          body: JSON.stringify({ error: 'that pid now belongs to a different process; refresh the list', code: 'identity_mismatch' }),
        },
      },
    })
    const app = page.locator(APP)
    await app.getByText('gimp', { exact: true }).first().click()
    await app.getByRole('button', { name: 'Force Quit' }).click()
    await page.getByRole('dialog').getByRole('button', { name: 'Force quit' }).click()

    await expect(app.getByRole('status')).toContainText(/different process/)
  })
})

test.describe('Activity Monitor — the four responsiveness categories', () => {
  test('shows a measured verdict only where a measurement exists', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const app = page.locator(APP)

    await app.getByRole('button', { name: /^Apps / }).click()

    // Matched on the BADGE (which carries a title), not on bare text: the
    // column header is also the word "Responding", and a locator that cannot
    // tell a header from a verdict is not testing the verdict.
    await expect(app.getByTitle('HTTP 200 in 41ms (method: http_probe)')).toHaveText('Responding')
    await expect(app.getByTitle('no reply within 3s (method: http_probe)')).toHaveText('Not responding')
    // streamed X11: no mechanism, so no verdict.
    await expect(app.getByTitle(/_NET_WM_PING/)).toHaveText('Cannot tell')

    // The reason is on the badge, not hidden in a log.
    await expect(app.getByTitle(/_NET_WM_PING/)).toBeVisible()

    // Built-ins are absent from a server-side list, and the footer says why
    // rather than leaving their absence to be read as a bug.
    await expect(app.getByText(/run inside this browser tab/)).toBeVisible()

    await page.screenshot({ path: `${SHOTS}/apps-responsiveness-dark.png` })
  })

  // D state is the one most likely to be mislabelled frozen. It is usually a
  // stalled disk, and it is the one state where Force Quit cannot work.
  test('explains D state as storage rather than badging it Not Responding', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const app = page.locator(APP)

    await expect(app.getByText('disk sleep', { exact: true })).toBeVisible()
    await expect(app.getByTitle(/waiting on storage/)).toBeVisible()
    // The process table must not claim an event-loop verdict anywhere.
    await expect(app.getByText('Not responding', { exact: true })).toHaveCount(0)
  })
})

test.describe('Activity Monitor — a failing backend', () => {
  // The defect this app shipped with: every /api/* service answers 5xx with a
  // body that parses cleanly, so a swallowed failure became an empty list and
  // the app told the user their box had no processes.
  test('says it could not read the box, instead of showing an empty process list', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page, {
      overrides: {
        'GET /api/system/processes': {
          status: 503, contentType: 'application/json',
          body: JSON.stringify({ error: 'cannot read /proc on this host', code: 'proc_unavailable' }),
        },
      },
    })
    const app = page.locator(APP)

    await expect(app.getByText('Could not read this from your box')).toBeVisible()
    await expect(app.getByText('cannot read /proc on this host')).toBeVisible()
    await expect(app.getByText(/HTTP 503/)).toBeVisible()
    // The old empty state must NOT be what the user sees.
    await expect(app.getByText('No processes found')).toHaveCount(0)

    await page.screenshot({ path: `${SHOTS}/proc-failure-dark.png` })
  })

  test('keeps one failure from clearing another feed', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page, {
      overrides: {
        'GET /api/proc/apps': {
          status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'admin only' }),
        },
      },
    })
    const app = page.locator(APP)

    // Processes are healthy…
    await expect(app.getByText('vulos-server', { exact: true }).first()).toBeVisible()
    // …and the Apps tab still reports its own failure.
    //
    // The tab is matched on its PREFIX, not on "Apps (3)": a failed feed
    // renders its count as a dash, because a count of 0 beside a failure is
    // the same "no processes" lie in miniature. A regex demanding digits
    // matched only while the failure had not landed yet, which made this test
    // pass or fail on timing.
    await app.getByRole('button', { name: /^Apps / }).click()
    await expect(app.getByText('admin only')).toBeVisible()
  })

  // The one screen that can explain a sick box must not be the screen that
  // refuses to render because part of it is sick.
  test('still lists processes when the telemetry socket is down', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page, { telemetry: false })
    const app = page.locator(APP)

    await expect(app.getByText(/telemetry connection to your box is down/)).toBeVisible()
    await expect(app.getByText('vulos-server', { exact: true }).first()).toBeVisible()
    await expect(app.getByText('postgres', { exact: true }).first()).toBeVisible()

    await page.screenshot({ path: `${SHOTS}/telemetry-down-dark.png` })
  })
})

test.describe('Activity Monitor — network', () => {
  test('shows a pid in the PID column, never a socket inode', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const app = page.locator(APP)

    await app.getByRole('button', { name: /^Network / }).click()
    await expect(app.getByText('vulos-server').first()).toBeVisible()
    await expect(app.getByText('812', { exact: true }).first()).toBeVisible()

    // No cell may show one of the fixture's inode numbers. That is what the
    // column used to contain, and six-digit inodes look like plausible pids.
    for (const inode of ['918273', '918290', '918311', '918400', '918455']) {
      await expect(app.getByText(inode, { exact: true })).toHaveCount(0)
    }
    // A socket with no owning process shows a dash, not a stand-in number.
    await expect(app.getByText('—', { exact: true }).first()).toBeVisible()

    await page.screenshot({ path: `${SHOTS}/network-dark.png` })
  })
})

test.describe('Activity Monitor — the graph history', () => {
  /**
   * The sample history must advance at the SOCKET's cadence, not the render's.
   *
   * It did not. `stats` was a fresh object built on every render and was the
   * history effect's only dependency, so the effect ran after every render,
   * appended a sample, and the state change re-rendered — a self-sustaining
   * loop, the same shape as the ResizeObserver bug already documented in this
   * file's nextGraphSize comment, surviving in the component's other effect.
   *
   * What made it survive review is what it LOOKS like: the 120-sample buffer
   * saturated within about two seconds, so all four sparklines drew a
   * full-width flat line at the current value. That reads as a quiet box.
   *
   * The fake socket ticks every 300ms, so about two seconds of app lifetime is
   * a handful of samples. The bound is generous — the failure mode is 120, not
   * 12.
   */
  test('advances at the socket cadence rather than the render cadence', async ({ page }) => {
    test.setTimeout(120_000)
    await bootActivity(page)
    const samples = async () => Number(await page.locator(`${APP} [data-samples]`).getAttribute('data-samples'))

    const first = await samples()
    expect(first, 'no samples arrived at all — the fake socket is not wired').toBeGreaterThan(0)
    expect(first, 'the 120-sample history saturated immediately: the effect is running per render').toBeLessThan(40)

    // And it must still be ADVANCING — a frozen counter would satisfy the
    // bound above while meaning the graphs stopped updating entirely.
    await page.waitForTimeout(1500)
    const second = await samples()
    expect(second, 'the sample count stopped advancing').toBeGreaterThan(first)
    expect(second - first, 'samples arrived far faster than the 300ms socket tick').toBeLessThan(30)
  })
})
