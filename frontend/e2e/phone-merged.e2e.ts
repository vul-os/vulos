// phone-merged.e2e.ts — the ONE contacts-and-calls surface in a real browser,
// against a deterministic fake box.
//
// WHAT MERGED, AND WHAT THAT DID TO THIS FILE. Vulos had two surfaces over one
// address book: `vulos-contacts` (editable cards) and `vulos-phone` (a dialler
// carrying its own read-only copy). They are now one component with PEOPLE on
// the front page, and both app ids render it. The assertions here that assumed
// the old vehicle — that the app opens on Recents, that its contacts tab is a
// read-only list of its own, that a modem-less box shows a dead end with an
// "Open contacts" button — have been rewritten against the new one. The
// PROPERTIES they protect are unchanged and are all still asserted below.
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
//
//   4. A box with NO modem — most Vulos boxes — gets a complete address book
//      and is not offered a dial pad or an SMS inbox it can never use.
//
//   5. The in-call bar reflects what the MODEM reports, never the fact that a
//      dial was posted.

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

// The EDITABLE cards, which are what the people pane reads. Distinct from the
// unified list above, which is the box's merged index (CardDAV + box SIM +
// pushed phone book) and is what puts names on call rows.
const CARDS = {
  contacts: [
    { uid: 'u1', name: 'Priya Naidoo', org: 'Kestrel Labs', title: '', note: '', emails: ['priya@example.org'], phones: ['+27 83 111 2222'] },
  ],
}

const NO_CALL = { active: false }

const ROUTES_OK: Routes = {
  'GET /api/telephony/status': json(MODEM_PRESENT),
  'GET /api/telephony/virtual/status': json({ configured: false, can_call: false }),
  'GET /api/telephony/calls': json(GSM_CALLS),
  'GET /api/telephony/call/active': json(NO_CALL),
  'GET /api/telephony/sms/threads': json([]),
  'GET /api/peering/call/history': json(PEER_CALLS),
  'GET /api/contacts/unified': json(CONTACTS),
  'GET /api/pim/contacts/cards': json(CARDS),
}

/** Move to a page of the merged surface. Contacts is where it opens. */
async function openTab(page: Page, name: 'Contacts' | 'Recents' | 'Keypad' | 'Messages') {
  await page.locator('[data-phone-app]').getByRole('tab', { name }).click()
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

    // Recents is where the call buttons live now that the surface opens on
    // people — the pages moved, the property did not.
    await openTab(page, 'Recents')

    // Every call button is disabled, and the reason is on the button itself.
    const callButtons = app(page).locator('button[aria-label^="Call"]')
    const n = await callButtons.count()
    expect(n).toBeGreaterThan(0)
    for (let i = 0; i < n; i++) {
      await expect(callButtons.nth(i)).toBeDisabled()
      expect(await callButtons.nth(i).getAttribute('title')).toContain('data/SMS only')
    }
  })

  test('a box with no modem opens straight onto a working address book', async ({ page }) => {
    // THE COMMON CASE. Most Vulos boxes have no modem, and on those this
    // surface has to be a good Contacts app — not a dialler apologising for
    // itself. It used to open on a dead end with an "Open contacts" escape
    // hatch; now the address book IS the app and the telephony pages are
    // simply not there.
    await bootPhone(page, 'dark', { 'GET /api/telephony/status': json(MODEM_NONE) })

    await expect(app(page)).toContainText('Priya Naidoo')

    // A dial pad and an SMS inbox could only ever be empty and could only ever
    // fail, so neither is offered at all.
    await expect(app(page).getByRole('tab', { name: 'Keypad' })).toHaveCount(0)
    await expect(app(page).getByRole('tab', { name: 'Messages' })).toHaveCount(0)
  })

  test('a box with no modem names the hardware to plug in, generically', async ({ page }) => {
    await bootPhone(page, 'dark', {
      'GET /api/telephony/status': json(MODEM_NONE),
      'GET /api/telephony/calls': json([]),
      'GET /api/peering/call/history': json([]),
    })

    // Recents is where "why is there no dialler here?" gets answered.
    await openTab(page, 'Recents')
    await expect(app(page)).toContainText('No SIM or modem on this box')
    await expect(app(page)).toContainText(/USB LTE/i)
    await expect(app(page)).toContainText(/ModemManager/i)
    expect((await app(page).innerText()).toLowerCase()).not.toContain('android')
  })

  test('a contact’s number says why it cannot be called, rather than pretending', async ({ page }) => {
    await bootPhone(page, 'dark', { 'GET /api/telephony/status': json(MODEM_NONE) })

    await app(page).getByText('Priya Naidoo').first().click()
    const detail = app(page).locator('[data-contact-detail]')
    await expect(detail).toBeVisible()
    await expect(detail.locator('[data-call-number]')).toBeDisabled()
    await expect(detail).toContainText(/No modem is connected to this box/i)
    expect((await detail.innerText()).toLowerCase()).not.toContain('android')
  })
})

test.describe('Recents is one list', () => {
  test('merges GSM and Vulos-to-Vulos calls in time order, and resolves names from the address book', async ({ page }) => {
    await bootPhone(page, 'dark')
    await openTab(page, 'Recents')

    // The GSM row's raw number is +27831112222; the contact stores it as
    // "+27 83 111 2222". Matching on the last 9 digits is what makes the call
    // log show a person rather than a string of digits.
    await expect(app(page)).toContainText('Priya Naidoo')
    await expect(app(page)).toContainText('Sipho Dlamini')
    // The peer row comes from a different service with no phone number at all.
    await expect(app(page)).toContainText('Thandi Mokoena')
    await expect(app(page)).toContainText('over Vulos')

    // Newest first, across BOTH sources: missed (5m) → peer (30m) → outgoing (2h).
    // A data hook, not a utility class: the row's font weight is now a
    // meaningful signal (missed calls are heavier), so a class-based selector
    // would silently stop matching the moment that changed — which is exactly
    // what it did.
    const names = await app(page).locator('[data-recent-name]').allInnerTexts()
    const order = names.filter((n) => /Priya|Thandi|Sipho/.test(n))
    expect(order[0]).toContain('Priya')
    expect(order[1]).toContain('Thandi')
    expect(order[2]).toContain('Sipho')
  })

  test('a peer row offers no redial, because a Vulos call has no number and cannot be placed', async ({ page }) => {
    await bootPhone(page, 'dark')
    await openTab(page, 'Recents')
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
    await openTab(page, 'Recents')

    await expect(app(page).getByRole('alert')).toBeVisible()
    await expect(app(page)).not.toContainText('No calls yet')
  })

  test('a 500 on the editable cards shows the unavailable state, NOT an empty book', async ({ page }) => {
    await bootPhone(page, 'dark', { 'GET /api/pim/contacts/cards': json({ error: 'boom' }, 502) })

    await expect(app(page)).toContainText(/Contacts unavailable/i)
    await expect(app(page)).not.toContainText('No contacts yet')
  })

  test('a 500 on the UNIFIED sources says the list is short, instead of hiding it', async ({ page }) => {
    // The nastier half of the same defect, and the one that was live: the
    // people pane's unified read ended in `.catch(() => [])`, so a broken box
    // silently dropped every contact that lives only on the box SIM or the
    // pushed phone book. The editable cards still render — this is a WARNING
    // on a working list, not a dead app — but the omission must be stated.
    await bootPhone(page, 'dark', { 'GET /api/contacts/unified': json({ error: 'boom' }, 500) })

    await expect(app(page)).toContainText('Priya Naidoo')
    await expect(app(page).getByRole('alert')).toContainText(/may be missing people/i)
  })

  test('an unreadable body (a JSON object where a list belongs) is an error, not an empty list', async ({ page }) => {
    // This is the exact failure mode of the raw-fetch narrowing pattern: the
    // service answers 200 with something that is not the documented shape, the
    // narrower maps it to [], and the app renders its designed empty state.
    await bootPhone(page, 'dark', {
      'GET /api/telephony/calls': json({ notAList: true }),
      'GET /api/peering/call/history': json([]),
    })
    await openTab(page, 'Recents')

    await expect(app(page).getByRole('alert')).toBeVisible()
    await expect(app(page)).not.toContainText('No calls yet')
  })
})

test.describe('the call that is happening right now', () => {
  test('the in-call bar is drawn from the MODEM, not from the dial we sent', async ({ page }) => {
    // The box reports no call, whatever we post. A bar driven by "we posted a
    // dial" would appear here and offer a Hang up that hangs up nothing.
    await bootPhone(page, 'dark')
    await page.route('**/api/telephony/call', (route) =>
      route.fulfill(json({ ok: true }) as Parameters<typeof route.fulfill>[0]))

    await app(page).getByText('Priya Naidoo').first().click()
    await app(page).locator('[data-call-number]').click()

    await expect(app(page).locator('[data-in-call-bar]')).toHaveCount(0)
  })

  test('a call the modem reports gets a bar and a Hang up that reaches the box', async ({ page }) => {
    await bootPhone(page, 'dark', {
      'GET /api/telephony/call/active': json({ active: true, number: '+27831112222', direction: 'outgoing', state: 'active' }),
    })

    const bar = app(page).locator('[data-in-call-bar]')
    await expect(bar).toBeVisible({ timeout: 10_000 })
    await expect(bar).toContainText('On a call')
    // Resolved against the address book: the modem reports +27831112222 and the
    // card stores "+27 83 111 2222".
    await expect(bar).toContainText('Priya Naidoo')

    const posts: string[] = []
    await page.route('**/api/telephony/call/hangup', async (route) => {
      posts.push(route.request().url())
      await route.fulfill(json({ ok: true }) as Parameters<typeof route.fulfill>[0])
    })
    await bar.locator('[data-call-hangup]').click()
    await expect.poll(() => posts.length).toBeGreaterThan(0)
  })

  test('a ringing inbound call offers Answer and Decline, not Hang up', async ({ page }) => {
    await bootPhone(page, 'dark', {
      'GET /api/telephony/call/active': json({ active: true, number: '+27831112222', direction: 'incoming', state: 'ringing-in' }),
    })

    const bar = app(page).locator('[data-in-call-bar]')
    await expect(bar).toBeVisible({ timeout: 10_000 })
    await expect(bar).toContainText('Incoming call')
    await expect(bar.locator('[data-call-answer]')).toBeVisible()
    await expect(bar.locator('[data-call-hangup]')).toHaveCount(0)
  })

  test('a box with no modem never draws an in-call bar', async ({ page }) => {
    // The poll is not even issued without a line — but the guarantee that
    // matters to a user is the absence of the bar, so that is what is asserted.
    await bootPhone(page, 'dark', {
      'GET /api/telephony/status': json(MODEM_NONE),
      'GET /api/telephony/call/active': json({ active: true, number: '+27831112222', direction: 'incoming', state: 'active' }),
    })

    await expect(app(page)).toContainText('Priya Naidoo')
    await expect(app(page).locator('[data-in-call-bar]')).toHaveCount(0)
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

    await openTab(page, 'Keypad')
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

    await openTab(page, 'Keypad')
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
    await openTab(page, 'Recents')
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
