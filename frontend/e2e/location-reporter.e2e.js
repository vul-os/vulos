// location-reporter.e2e.js — the shell/PWA geolocation reporter in a REAL browser.
//
// src/core/location/reporter.js feeds the box the browser's position by POSTing
// to /api/location. It is a dependency-free module (navigator.geolocation + fetch
// only) with no shipped UI wiring yet, so there is no shell surface to navigate
// to. To still exercise the REAL module code in a real browser, we build a tiny
// self-contained harness page: it stubs navigator.geolocation with a controllable
// fake, inlines the ACTUAL reporter.js source as a module, and adds a small
// "consuming view" that reads GET /api/location. The reporter runs against the
// stub + the browser's real fetch, which our page.route intercepts.
//
// The pure throttle/distance helpers and the fetchImpl seam are covered by the
// jsdom unit suite (src/__tests__/locationReporter.test.js); this spec proves the
// browser-level contract the unit suite can't: that a real same-origin POST
// carries the session cookie (credentials), that the throttle holds across two
// real fixes, and that a fetched fix renders in a consuming view.
//
// What each test verifies:
//   • a geolocation fix POSTs {lat,lng,accuracy,altitude,heading,speed} to
//     /api/location WITH the session cookie (credentials: 'include')
//   • a rapid, identical second fix is throttled — no double-POST
//   • GET /api/location surfaces a fix into a consuming view

import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
// The SHIPPED reporter — run verbatim in-browser (it has no imports, so it drops
// cleanly into an inline module script).
const REPORTER_SRC = readFileSync(join(__dirname, '..', 'src', 'core', 'location', 'reporter.js'), 'utf8')

const HARNESS_PATH = '/loc-harness'
const SESSION_COOKIE = 'vulos_session=LOC-SESSION-MARKER'

// The harness page. A classic script installs a controllable navigator.geolocation
// stub BEFORE the module script runs (so startLocationReporting() picks it up),
// exposing window.__geo.fire(coords) to drive fixes. The module script inlines the
// real reporter and wires a consuming view.
const HARNESS_HTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>location harness</title></head>
<body>
<button id="load">Load location</button>
<pre id="last-fix">none</pre>
<pre id="report-status">idle</pre>
<script>
  // "stubs navigator.geolocation" — a fake that captures the reporter's success/
  // error callbacks and lets the test fire fixes on demand.
  (function () {
    var successCb = null, errorCb = null;
    var stub = {
      watchPosition: function (ok, err) { successCb = ok; errorCb = err; return 1; },
      clearWatch: function () {},
    };
    Object.defineProperty(navigator, 'geolocation', { value: stub, configurable: true });
    window.__geo = {
      fire: function (coords) { if (successCb) successCb({ coords: coords }); },
      error: function (e) { if (errorCb) errorCb(e); },
    };
  })();
</script>
<script type="module">
${REPORTER_SRC}
  // Consuming view: fetch the current fix and render it.
  document.getElementById('load').addEventListener('click', function () {
    fetch('/api/location', { credentials: 'include' })
      .then(function (r) { return r.json(); })
      .then(function (d) { document.getElementById('last-fix').textContent = 'fix:' + d.lat + ',' + d.lng; })
      .catch(function () { document.getElementById('last-fix').textContent = 'error'; });
  });
  // Start the REAL reporter against the stubbed geolocation + the browser's real
  // fetch (intercepted by page.route). No opts => navigator.geolocation + fetch.
  var started = startLocationReporting();
  document.getElementById('report-status').textContent = started ? 'reporting' : 'inactive';
</script>
</body></html>`

async function serveHarness(page, { onPost, getSpec } = {}) {
  await page.route(`**${HARNESS_PATH}`, (route) =>
    route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: HARNESS_HTML }),
  )
  await page.route('**/api/location', async (route) => {
    const req = route.request()
    if (req.method() === 'POST') {
      if (onPost) onPost(req)
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) })
    }
    // GET — the consuming view's read path.
    const body = getSpec || { lat: -26.2, lng: 28.05, accuracy: 12, ts: Date.now() }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
}

async function boot(page, opts) {
  await serveHarness(page, opts)
  await page.goto(HARNESS_PATH)
  // The reporter armed its watch against our stub.
  await expect(page.locator('#report-status')).toHaveText('reporting', { timeout: 15_000 })
  // Plant a session cookie on this origin — a same-origin credentialed POST must
  // carry it.
  await page.evaluate((c) => { document.cookie = c + '; path=/' }, SESSION_COOKIE)
}

test('a geolocation fix POSTs the fix to /api/location with credentials', async ({ page }) => {
  const posts = []
  await boot(page, { onPost: (req) => posts.push(req) })

  await page.evaluate(() => window.__geo.fire({
    latitude: -26.2, longitude: 28.05, accuracy: 12, altitude: 100, heading: 90, speed: 3,
  }))

  await expect.poll(() => posts.length, { timeout: 5_000 }).toBe(1)

  // The wire body is the {lat,lng,accuracy,altitude,heading,speed} shape.
  expect(posts[0].postDataJSON()).toEqual({
    lat: -26.2, lng: 28.05, accuracy: 12, altitude: 100, heading: 90, speed: 3,
  })
  // credentials: 'include' → the same-origin session cookie rides along. This is
  // the browser-level proof the unit suite (which asserts init.credentials) can't
  // give: /api/location is auth-scoped, so a cookieless POST would 401.
  const cookie = posts[0].headers()['cookie'] || ''
  expect(cookie, `POST /api/location must carry the session cookie, saw: "${cookie}"`)
    .toContain('LOC-SESSION-MARKER')
})

test('a rapid identical second fix is throttled — no double-POST', async ({ page }) => {
  const posts = []
  await boot(page, { onPost: (req) => posts.push(req) })

  // Two identical fixes back to back: default throttle is 10s / 25m, so the second
  // (same spot, arriving immediately) must not POST.
  await page.evaluate(() => {
    window.__geo.fire({ latitude: 1, longitude: 1, accuracy: 5 })
    window.__geo.fire({ latitude: 1, longitude: 1, accuracy: 5 })
  })

  await expect.poll(() => posts.length, { timeout: 5_000 }).toBe(1)
  // Give any stray second POST a chance to land, then confirm it never did.
  await page.waitForTimeout(500)
  expect(posts.length).toBe(1)
})

test('GET /api/location surfaces a fix into a consuming view', async ({ page }) => {
  await boot(page, { getSpec: { lat: 51.5074, lng: -0.1278, accuracy: 8, ts: Date.now() } })

  await page.getByRole('button', { name: 'Load location' }).click()

  await expect(page.locator('#last-fix')).toHaveText('fix:51.5074,-0.1278', { timeout: 5_000 })
})
