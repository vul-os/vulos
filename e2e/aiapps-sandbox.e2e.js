// aiapps-sandbox.e2e.js — BROWSER-LEVEL isolation of AI-generated apps.
//
// AI apps are untrusted, model-authored HTML. The shell renders them in an
// iframe sandboxed WITHOUT `allow-same-origin`, so the browser forces the app
// document into a unique/opaque origin: it cannot read the OS origin's cookies,
// reach the top (shell) document, or fetch the OS API. The served HTML also
// carries a `sandbox` CSP (default-src 'none') as defense-in-depth.
//
// This can only be proven in a REAL browser (jsdom does not enforce iframe
// sandbox origins). We launch a real AI app whose OWN inline script probes what
// it can see, and assert — from both the parent frame (the sandbox attribute)
// and inside the opaque frame (the probe's recorded outcomes) — that every path
// back to the OS origin is closed.

import { test, expect } from '@playwright/test'
import { installBackend } from './mock-backend.js'

// The AI app id + title the test launches. id must match ^[a-z0-9][a-z0-9-]*$
// (the backend charset) and start with a letter so it is a clean palette match.
const APP_ID = 'ai-sandbox-probe'
const APP_TITLE = 'Sandbox Probe'

// The probe page. Its inline script records, into its own DOM, whether it can
// (1) observe a non-opaque origin, (2) read the OS cookie, (3) reach the top
// document, or (4) fetch the OS API. Everything must be BLOCKED.
const PROBE_HTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>${APP_TITLE}</title></head>
<body>
<pre id="origin">pending</pre>
<pre id="cookie">pending</pre>
<pre id="parent">pending</pre>
<pre id="api">pending</pre>
<script>
  // (1) Our own origin. An opaque (sandboxed, no allow-same-origin) document
  // reports origin "null".
  document.getElementById('origin').textContent = 'ORIGIN:' + String(window.origin);

  // (2) The OS session cookie must be unreachable. In an opaque origin,
  // document.cookie access throws; either way the OS secret must not appear.
  try {
    document.getElementById('cookie').textContent = 'COOKIE_OK:[' + document.cookie + ']';
  } catch (e) {
    document.getElementById('cookie').textContent = 'COOKIE_BLOCKED:' + (e && e.name || 'err');
  }

  // (3) The top (shell) document must be cross-origin — reading its location
  // throws a SecurityError.
  try {
    var h = window.top.location.href;
    document.getElementById('parent').textContent = 'PARENT_REACHED:' + h;
  } catch (e) {
    document.getElementById('parent').textContent = 'PARENT_BLOCKED:' + (e && e.name || 'err');
  }

  // (4) A fetch to the OS API must be blocked (CSP default-src 'none' +
  // opaque-origin credentials cannot ride along).
  fetch('/api/auth/me', { credentials: 'include' })
    .then(function (r) { return r.text(); })
    .then(function (t) { document.getElementById('api').textContent = 'API_REACHED:' + t.slice(0, 60); })
    .catch(function (e) { document.getElementById('api').textContent = 'API_BLOCKED:' + (e && e.name || 'err'); });
</script>
</body></html>`

// The exact defense-in-depth headers the real backend attaches to served
// AI-app HTML (backend/cmd/server/routes_aiapps_security.go aiAppsSecurityHeaders).
const AIAPP_HEADERS = {
  'Content-Security-Policy':
    "sandbox allow-scripts allow-forms allow-popups; default-src 'none'; " +
    "script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; " +
    "font-src data:; frame-ancestors 'self'; base-uri 'none'; form-action 'none'",
  'X-Frame-Options': 'SAMEORIGIN',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
  'Cache-Control': 'no-store',
}

async function boot(page) {
  await installBackend(page, {
    // Surface one launchable AI app in the registry.
    'GET /api/ai-apps': {
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([{ id: APP_ID, title: APP_TITLE, has_html: 'true' }]),
    },
    // Serve the probe HTML with the real sandbox CSP headers.
    [`GET /api/ai-apps/${APP_ID}/html`]: {
      status: 200,
      contentType: 'text/html; charset=utf-8',
      headers: AIAPP_HEADERS,
      body: PROBE_HTML,
    },
  })
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
  // Plant an OS-origin cookie the sandboxed app must never be able to read.
  await page.evaluate(() => { document.cookie = 'os_session=SUPERSECRET-OS-TOKEN; path=/' })
}

async function launch(page, appName) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(appName)
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
}

test('AI app renders in an iframe with no allow-same-origin (opaque origin)', async ({ page }) => {
  await boot(page)
  await launch(page, APP_TITLE)

  // The AI-app iframe exists and is sandboxed.
  const frameEl = page.locator('iframe[src*="ai-apps"]')
  await expect(frameEl).toBeVisible({ timeout: 10_000 })

  const sandbox = await frameEl.getAttribute('sandbox')
  expect(sandbox, 'AI-app iframe must be sandboxed').toBeTruthy()
  // The load-bearing invariant: NO allow-same-origin (that would re-grant the OS origin).
  expect(sandbox).not.toContain('allow-same-origin')
  // Scripts are allowed so the app still runs.
  expect(sandbox).toContain('allow-scripts')
})

test('the sandboxed AI app cannot reach the OS origin (cookie / top / API all blocked)', async ({ page }) => {
  await boot(page)
  await launch(page, APP_TITLE)

  const frame = page.frameLocator('iframe[src*="ai-apps"]')

  // (1) The app document is at an opaque origin.
  await expect(frame.locator('#origin')).toHaveText('ORIGIN:null', { timeout: 10_000 })

  // (2) The OS session cookie is NOT visible to the app (blocked or empty — never
  // the secret value).
  const cookie = await frame.locator('#cookie').textContent()
  expect(cookie, `app must not read the OS cookie: ${cookie}`).not.toContain('SUPERSECRET-OS-TOKEN')

  // (3) The app cannot reach the top (shell) document — cross-origin SecurityError.
  await expect(frame.locator('#parent')).toContainText('PARENT_BLOCKED', { timeout: 10_000 })

  // (4) The app cannot fetch the OS API (CSP default-src 'none' + opaque origin).
  await expect(frame.locator('#api')).toContainText('API_BLOCKED', { timeout: 10_000 })
})
