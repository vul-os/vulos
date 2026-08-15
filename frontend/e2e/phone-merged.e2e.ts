// phone-merged.e2e.ts — the merged Phone app (Recents + Contacts + Keypad +
// Messages) in a real browser, against a deterministic fake box.
//
// The properties these tests exist to protect, in order of how badly they
// failed before:
//
//   1. The app must work on a box whose modem is NOT an Android phone. The
//      previous version spoke only to the Android native bridge and told every
//      USB-LTE-stick owner that Phone "only works inside the Vulos Android app".
//      No browser here has a native bridge, so if the app is Android-gated at
//      all, every test in this file fails.
//
//   2. A dead service must never render as a designed empty state. "No calls
//      yet" and "the telephony service is returning 500s" must not look the
//      same, and for a dialler that distinction is the whole product.
//
//   3. A 200 response carrying {"error": …} is a FAILURE. The box answers
//      POST /api/telephony/call with status 200 and an error body when there is
//      no modem, so checking res.ok alone reports a call that never happened as
//      a success.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { launchApp } from './contrast-scan'

const NOW = Math.floor(Date.now() / 1000)

// A live, voice-capable modem attached to THE BOX. Nothing about this is
// Android — it is what ModemManager reports for a USB LTE stick.
const MODEM_PRESENT = {
  available: true, state: 'registered', signal_quality: 72,
  operator: 'Test Net', number: '+27830000001', voice: true,
}
const MODEM_NONE = { available: false }
const MODEM_DATA_ONLY = { ...MODEM_PRESENT, voice: false }

const GSM_CALLS = [
  { id: 'c1', number: '+27831112222', direction: 'missed', ts: NOW - 300, duration: 0 },
  { id: 'c2', number: '+27835554444', direction: 'outgoing', ts: NOW - 7200, duration: 95 },
]

const PEER_CALLS = [
  {
    id: 'p1', peer_id: 'vulos:ed25519:abc', peer_display: 'Thandi Mokoena',
    direction: 'inbound', status: 'completed',
    started_at: new Date((NOW - 1800) * 1000).toISOString(), duration_sec: 240,
  },
]

const CONTACTS = {
  contacts: [
    { id: 'u1', name: 'Priya Naidoo', phones: ['+27 83 111 2222'], emails: ['priya@example.org'], org: 'Kestrel Labs', sources: ['vulos'] },
    { id: 'u2', name: 'Sipho Dlamini', phones: ['0835554444'], emails: [], org: '', sources: ['box-sim'] },
  ],
  sources_active: ['vulos', 'box-sim'],
}

type Routes = Record<string, unknown>

const ROUTES_OK: Routes = {
  'GET /api/telephony/status': json(MODEM_PRESENT),
  'GET /api/telephony/virtual/status': json({ configured: false, can_call: false }),
  'GET /api/telephony/calls': json(GSM_CALLS),
  'GET /api/telephony/sms/threads': json([]),
  'GET /api/peering/call/history': json(PEER_CALLS),
  'GET /api/contacts/unified': json(CONTACTS),
}

async function bootPhone(page: Page, theme: 'dark' | 'light', overrides: Routes = {}) {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  const backend = await installBackend(page, { ...ROUTES_OK, ...overrides })
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
  await launchApp(page, 'Phone')
  await expect(page.locator('[data-phone-app]')).toBeVisible({ timeout: 10_000 })
  return backend
}

const app = (page: Page) => page.locator('[data-phone-app]')

test.describe('Phone works on a box whose modem is not a phone', () => {
  test('shows the BOX modem as the line, and never tells the user to go find an Android phone', async ({ page }) => {
    await bootPhone(page, 'dark')

    // The line bar names the box's own modem and its operator.
    await expect(app(page)).toContainText('Box modem')
    await expect(app(page)).toContainText('Test Net')

    // The old app's central claim must be gone. There is no native bridge in a
    // browser, so if any Android gating survived, this is what would render.
    const text = (await app(page).innerText()).toLowerCase()
    expect(text).not.toContain('android')
    expect(text).not.toContain('no sim on this device')
  })

  test('a data/SMS-only modem says so and disables dialling instead of failing on press', async ({ page }) => {
    await bootPhone(page, 'dark', { 'GET /api/telephony/status': json(MODEM_DATA_ONLY) })

    await expect(app(page)).toContainText('SMS only')

    // Every call button is disabled, and the reason is on the button itself.
    const callButtons = app(page).locator('button[aria-label^="Call"]')
    const n = await callButtons.count()
    expect(n).toBeGreaterThan(0)
    for (let i = 0; i < n; i++) {
      await expect(callButtons.nth(i)).toBeDisabled()
      expect(await callButtons.nth(i).getAttribute('title')).toContain('data/SMS only')
    }
  })

  test('a box with no modem names the hardware to plug in, and still hands over contacts', async ({ page }) => {
    await bootPhone(page, 'dark', { 'GET /api/telephony/status': json(MODEM_NONE) })

    await expect(app(page)).toContainText('No SIM or modem on this box')
    await expect(app(page)).toContainText(/USB LTE/i)
    await expect(app(page)).toContainText(/ModemManager/i)
    expect((await app(page).innerText()).toLowerCase()).not.toContain('android')

    // The address book is not hardware-dependent, so it is still reachable.
    await app(page).getByRole('button', { name: 'Open contacts' }).click()
    await expect(app(page)).toContainText('Priya Naidoo')
  })
})

test.describe('Recents is one list', () => {
  test('merges GSM and Vulos-to-Vulos calls in time order, and resolves names from the address book', async ({ page }) => {
    await bootPhone(page, 'dark')

    // The GSM row's raw number is +27831112222; the contact stores it as
    // "+27 83 111 2222". Matching on the last 9 digits is what makes the call
    // log show a person rather than a string of digits.
    await expect(app(page)).toContainText('Priya Naidoo')
    await expect(app(page)).toContainText('Sipho Dlamini')
    // The peer row comes from a different service with no phone number at all.
    await expect(app(page)).toContainText('Thandi Mokoena')
    await expect(app(page)).toContainText('over Vulos')

    // Newest first, across BOTH sources: missed (5m) → peer (30m) → outgoing (2h).
    const names = await app(page).locator('li button span.font-medium').allInnerTexts()
    const order = names.filter((n) => /Priya|Thandi|Sipho/.test(n))
    expect(order[0]).toContain('Priya')
    expect(order[1]).toContain('Thandi')
    expect(order[2]).toContain('Sipho')
  })

  test('a peer row offers no redial, because a Vulos call has no number and cannot be placed', async ({ page }) => {
    await bootPhone(page, 'dark')
    const peerRow = app(page).locator('li').filter({ hasText: 'Thandi Mokoena' })
    await expect(peerRow.locator('button[aria-label^="Call"]')).toHaveCount(0)
    // The GSM rows next to it DO offer it.
    await expect(app(page).locator('li').filter({ hasText: 'Priya Naidoo' })
      .locator('button[aria-label^="Call"]')).toHaveCount(1)
  })
})

test.describe('a broken box never wears the face of an empty one', () => {
  test('a 500 on the call log shows an error, NOT "No calls yet"', async ({ page }) => {
    await bootPhone(page, 'dark', {
      'GET /api/telephony/calls': json({ error: 'internal' }, 500),
      'GET /api/peering/call/history': json([]),
    })

    await expect(app(page).getByRole('alert')).toBeVisible()
    await expect(app(page)).not.toContainText('No calls yet')
  })

  test('a 500 on contacts shows an error, NOT "No contacts yet"', async ({ page }) => {
    await bootPhone(page, 'dark', { 'GET /api/contacts/unified': json({ error: 'boom' }, 500) })
    await app(page).getByRole('tab', { name: 'Contacts' }).click()

    await expect(app(page).getByRole('alert')).toBeVisible()
    await expect(app(page)).not.toContainText('No contacts yet')
  })

  test('an unreadable body (a JSON object where a list belongs) is an error, not an empty list', async ({ page }) => {
    // This is the exact failure mode of the raw-fetch narrowing pattern: the
    // service answers 200 with something that is not the documented shape, the
    // narrower maps it to [], and the app renders its designed empty state.
    await bootPhone(page, 'dark', {
      'GET /api/telephony/calls': json({ notAList: true }),
      'GET /api/peering/call/history': json([]),
    })

    await expect(app(page).getByRole('alert')).toBeVisible()
    await expect(app(page)).not.toContainText('No calls yet')
  })
})

test.describe('dialling', () => {
  test('places the call on the box modem with the number that was dialled', async ({ page }) => {
    await bootPhone(page, 'dark')

    // Registered AFTER bootPhone on purpose: Playwright matches routes
    // last-registered-first, so installBackend's `**/api/**` catch-all would
    // swallow this one if it went in earlier — and the test would then assert
    // nothing while reporting green.
    const posts: string[] = []
    await page.route('**/api/telephony/call', async (route) => {
      posts.push(route.request().postData() || '')
      await route.fulfill(json({ ok: true }) as Parameters<typeof route.fulfill>[0])
    })

    await app(page).getByRole('tab', { name: 'Keypad' }).click()
    for (const k of ['0', '8', '3', '1', '2', '3', '4', '5', '6', '7']) {
      await app(page).getByRole('button', { name: k, exact: true }).click()
    }
    await app(page).locator('button[aria-label^="Call"]').last().click()

    await expect.poll(() => posts.length).toBeGreaterThan(0)
    expect(JSON.parse(posts[0])).toEqual({ to: '0831234567' })
  })

  test('a 200 response carrying {"error"} is reported as a failure, not a placed call', async ({ page }) => {
    // THE TRAP. handlers.go answers POST /api/telephony/call with HTTP 200 and
    // {"error":"telephony: no modem"}. res.ok is true. Anything that trusts
    // res.ok alone tells the user the call went through.
    await bootPhone(page, 'dark', {
      'POST /api/telephony/call': json({ error: 'telephony: no modem' }),
    })

    await app(page).getByRole('tab', { name: 'Keypad' }).click()
    for (const k of ['0', '8', '3', '1', '2', '3', '4', '5', '6', '7']) {
      await app(page).getByRole('button', { name: k, exact: true }).click()
    }
    await app(page).locator('button[aria-label^="Call"]').last().click()

    const alert = app(page).getByRole('alert')
    await expect(alert).toBeVisible()
    await expect(alert).toContainText(/No modem is connected/i)
  })
})

test.describe('layout follows the app’s own width, not the viewport', () => {
  test('DRAGGING the window narrow flips it to the phone layout, on an unchanged 1600px viewport', async ({ page }) => {
    // This is the case a CSS media query gets wrong and cannot be made to get
    // right: the viewport stays 1600px the whole time and only the window is
    // resized, so anything keyed off the viewport keeps rendering the desktop
    // two-pane layout inside a 420px rail.
    await page.setViewportSize({ width: 1600, height: 1000 })
    await bootPhone(page, 'dark')

    const el = app(page)
    const win = page.locator('[data-window-id]').filter({ has: el })
    await expect.poll(() => el.getAttribute('data-phone-size')).not.toBe('narrow')

    const drag = async (toWidth: number) => {
      const box = await win.boundingBox()
      if (!box) throw new Error('window has no box')
      await page.mouse.move(box.x + box.width - 3, box.y + box.height - 3)
      await page.mouse.down()
      await page.mouse.move(box.x + toWidth, box.y + box.height - 3, { steps: 12 })
      await page.mouse.up()
    }

    await drag(1180)
    await expect.poll(() => el.getAttribute('data-phone-size'), { timeout: 10_000 }).toBe('wide')
    // Wide is the two-pane layout: a list rail AND a detail pane.
    await expect(el).toContainText('Pick a call')

    await drag(420)
    await expect.poll(() => el.getAttribute('data-phone-size'), { timeout: 10_000 }).toBe('narrow')
    expect(page.viewportSize()?.width).toBe(1600)

    // Bottom tab bar, thumb-reachable, is the narrow signature.
    const tabsBox = await el.getByRole('tablist').boundingBox()
    const appBox = await el.boundingBox()
    expect(tabsBox && appBox && tabsBox.y > appBox.y + appBox.height / 2).toBe(true)
  })
})
