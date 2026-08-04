// phone-app.e2e.js — the Phone (telephony) app surface in a REAL browser.
//
// The Phone app is a self-contained static surface (apps/phone/index.html) that
// talks to the Vulos Go telephony service at /api/telephony/*. Unlike the React
// builtins, it is NOT part of the vite shell bundle, so we serve its shipped
// HTML directly (read off disk) by intercepting the /app/phone/ route, then mock
// every telephony endpoint — and the WebSocket feed — at the browser layer.
//
// Loading it as the TOP-LEVEL document (its own origin = shell origin) is
// deliberate: inside the shell it runs opaque-sandboxed (ORIGIN-01) where its
// same-origin /api fetches would be blocked. This spec exercises the app's real
// logic — thread rendering, the send-SMS wire body, an inbound SMS over WS, and
// the modem-absent state — against a deterministic fake modem.
//
// What each test verifies:
//   • the SMS thread list renders from GET /api/telephony/sms/threads
//   • sending an SMS POSTs {to, body} to /api/telephony/sms/send
//   • an inbound `sms_received` frame over the WS feed surfaces in the UI
//   • a "no modem" state renders when /api/telephony/status reports none

import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
// The SHIPPED phone surface — we serve exactly what the app store would.
const PHONE_HTML = readFileSync(join(__dirname, '..', 'apps', 'phone', 'index.html'), 'utf8')

const NOW = Math.floor(Date.now() / 1000)

// A healthy, modem-present status. We return several truthy shapes so whichever
// field the surface's modem-gate reads (present / available / modem / state),
// it sees a live modem. ASSUMPTION: the phone surface fetches /api/telephony/status
// on load and gates the messaging UI on a present modem. Mocking it here is
// harmless if the surface doesn't (yet) consult it.
const STATUS_PRESENT = {
  present: true, available: true, modem: true, state: 'registered',
  operator: 'Test Net', signal: 80, number: '+15550000000',
}

// One SMS thread with a distinctive contact name + preview.
const THREAD_NUMBER = '+15551230001'
const THREADS = [
  { number: THREAD_NUMBER, contact_name: 'Alice Rivera', last_body: 'See you tomorrow', last_ts: NOW, unread_count: 1 },
]
const THREAD_MESSAGES = [
  { direction: 'incoming', body: 'See you tomorrow', ts: NOW, status: 'delivered' },
]

function json(body, status = 200) {
  return { status, contentType: 'application/json', body: JSON.stringify(body) }
}

// Serve the phone HTML (and a stub for its stylesheet) on the app route, and wire
// the telephony REST surface. `overrides` (keyed by "METHOD /path") and
// `statusSpec` let individual tests drive the modem-present / modem-absent cases.
async function servePhone(page, { statusSpec = json(STATUS_PRESENT), overrides = {} } = {}) {
  // The app document + its co-located assets (vulos-tokens.css). Anything that
  // isn't a known asset falls through to index.html (the app's own SPA fallback).
  await page.route('**/app/phone/**', (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('.css')) {
      return route.fulfill({ status: 200, contentType: 'text/css', body: '' })
    }
    return route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: PHONE_HTML })
  })

  const table = {
    'GET /api/telephony/status': statusSpec,
    'GET /api/telephony/sms/threads': json(THREADS),
    // The app builds this path with encodeURIComponent(number), so the leading
    // "+" arrives as "%2B" — and URL.pathname (used for `key` below) does not
    // decode it. Keying on the raw "+" never matched, so the thread fetch fell
    // through to the empty-object default and the app got a non-array body.
    ['GET /api/telephony/sms/thread/' + encodeURIComponent(THREAD_NUMBER)]: json(THREAD_MESSAGES),
    'GET /api/telephony/calls': json([]),
    ...overrides,
  }

  await page.route('**/api/telephony/**', async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname
    const key = `${req.method()} ${path}`
    let spec = table[key]
    if (typeof spec === 'function') spec = spec(req)
    // Unmocked telephony call → empty 200 rather than a hung request.
    if (!spec) spec = json({})
    await route.fulfill(spec)
  })
}

test('renders the SMS thread list from the backend', async ({ page }) => {
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await servePhone(page)
  await page.goto('/app/phone/')

  // The thread list hydrates from GET /api/telephony/sms/threads.
  await expect(page.getByText('Alice Rivera')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('See you tomorrow')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('sending an SMS POSTs {to, body} to the send endpoint', async ({ page }) => {
  let sentBody = null
  await servePhone(page, {
    overrides: {
      'POST /api/telephony/sms/send': (req) => {
        sentBody = req.postDataJSON()
        return json({ ok: true, status: 'sent' })
      },
    },
  })
  await page.goto('/app/phone/')

  // Open the thread, then compose + send.
  await page.getByText('Alice Rivera').click()
  const compose = page.getByPlaceholder('Message…')
  await expect(compose).toBeVisible({ timeout: 10_000 })
  await compose.fill('On my way')
  // The round send control (➤). Fall back to Enter-to-send if the glyph button
  // can't be resolved by role.
  await page.locator('#send-btn').click()

  await expect.poll(() => sentBody, { timeout: 5_000 }).toEqual({ to: THREAD_NUMBER, body: 'On my way' })
})

test('an inbound SMS over the WebSocket feed surfaces in the UI', async ({ page }) => {
  // Mock the telephony WS: we act as the server and push a frame to the client.
  let telWs = null
  await page.routeWebSocket('**/api/telephony/ws', (ws) => { telWs = ws })

  await servePhone(page)
  await page.goto('/app/phone/')

  // Wait for the app to open the socket (its status chip flips to "Live") and the
  // route handler to hand us the server end.
  await expect(page.locator('#ws-label')).toHaveText('Live', { timeout: 15_000 })
  await expect.poll(() => !!telWs, { timeout: 5_000 }).toBe(true)

  // Push an inbound SMS. handleWSMessage renders a toast "SMS from <from>: <body>"
  // (and refreshes the thread list).
  telWs.send(JSON.stringify({ type: 'sms_received', from: '+15559998888', body: 'Ping from the void' }))

  await expect(page.getByText(/SMS from \+15559998888: Ping from the void/)).toBeVisible({ timeout: 5_000 })
})

// NOT IMPLEMENTED — this is a specification, not a passing guard.
//
// apps/phone/index.html contains no modem-status handling at all (`grep -i
// modem apps/phone/index.html` returns nothing): with no modem the surface
// renders an ordinary empty messaging UI rather than saying why it is empty.
//
// It is marked fixme rather than deleted so the gap stays recorded, and rather
// than left failing so that a red run means a real regression. Playwright
// reports it as an expected failure. To close it: render an explicit
// modem-absent affordance driven by GET /api/telephony/status, then remove
// this marker — do not weaken the assertion.
test.fixme('renders a no-modem state when the modem is unavailable', async ({ page }) => {
  // ASSUMPTION: with no modem present, the phone surface renders an explicit
  // "no modem" affordance rather than an empty, dead messaging UI. We report the
  // modem absent via /api/telephony/status and assert the honest empty state with
  // a resilient text matcher (the exact copy may vary).
  await servePhone(page, {
    statusSpec: json({ present: false, available: false, modem: false, state: 'absent' }),
    // With no modem the thread feed is empty too — mirror that so the assertion
    // pins the modem-absent affordance, not incidental thread content.
    overrides: { 'GET /api/telephony/sms/threads': json([]) },
  })
  await page.goto('/app/phone/')

  await expect(
    page.getByText(/no modem|modem (not|is not|unavailable|absent)|no cellular|no sim|insert a sim/i),
  ).toBeVisible({ timeout: 15_000 })
})
