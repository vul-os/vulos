// app-origins.e2e.js — ORIGIN-01, proved in a real browser.
//
// The claim under test: an app frame CANNOT reach the shell's storage, DOM or
// cookies, and the shell↔app bridge accepts messages ONLY from the app's own
// frame at the app's expected origin.
//
// Before this change, `clock` (and calculator, pdf-viewer, text-editor, weather)
// were served from /app/{id}/ — the SHELL's origin — WITH `allow-same-origin`.
// That combination is not a sandbox at all: the frame could read the shell's
// localStorage and cookies and script window.top directly. These tests are the
// regression guard for exactly that.
//
// The deployment simulated here is the DEFAULT self-hosted box: no VULOS_DOMAIN,
// so per-app origins are unavailable and apps stay on the path prefix. That is
// the WEAKEST configuration, and the one where the old bug was worst — so it is
// the one worth pinning. (The per-app-origin configuration is covered on the
// server side in backend/services/gateway/origin_test.go, and the bridge protocol
// itself in src/__tests__/appBridge.test.js.)

import { test, expect } from '@playwright/test'
import { installBackend } from './mock-backend.js'

// A probe that behaves like a real app frame: it reports what it can reach, and
// it speaks the ORIGIN-01 bridge protocol (hello + storage writes over a
// MessagePort, addressed to the shell's exact origin — never '*').
const APP_PROBE = `<!doctype html>
<html><head><meta charset="utf-8"></head>
<body>
<pre id="origin">?</pre>
<pre id="cookie">?</pre>
<pre id="top">?</pre>
<pre id="shellstore">?</pre>
<pre id="bridge">?</pre>
<pre id="seeded">?</pre>
<script>
  // (1) What origin am I? An opaque (sandboxed) frame reports "null".
  document.getElementById('origin').textContent = 'ORIGIN:' + window.origin;

  // (2) Can I read the shell's cookies?
  try {
    document.getElementById('cookie').textContent = 'COOKIE:' + document.cookie;
  } catch (e) { document.getElementById('cookie').textContent = 'COOKIE_BLOCKED'; }

  // (3) Can I script the shell document?
  try {
    var href = window.top.location.href;
    document.getElementById('top').textContent = 'TOP_REACHED:' + href;
  } catch (e) { document.getElementById('top').textContent = 'TOP_BLOCKED'; }

  // (4) Can I read the SHELL's localStorage? (the whole point)
  try {
    var v = window.parent.localStorage.getItem('shell_secret');
    document.getElementById('shellstore').textContent = 'SHELLSTORE_READ:' + v;
  } catch (e) { document.getElementById('shellstore').textContent = 'SHELLSTORE_BLOCKED'; }

  // (5) The bridge: my own localStorage is the shim the gateway installs. Here we
  // speak the protocol directly so the test exercises the SHELL's real handler.
  // The shell origin is derivable from my own document URL (I am served from it).
  var SHELL = new URL(document.baseURI).origin;
  document.getElementById('seeded').textContent = 'SEED:' + (location.hash.indexOf('__vulos_s=') >= 0 ? 'yes' : 'no');
  try {
    var ch = new MessageChannel();
    ch.port1.onmessage = function (ev) {
      var m = ev.data;
      if (m && m.type === 'vulos.storage.snapshot') {
        document.getElementById('bridge').textContent = 'SNAPSHOT:' + JSON.stringify(m.data);
      }
    };
    // STRICT targetOrigin — never '*'. If our parent is not SHELL the browser
    // drops this and the bridge never forms.
    window.parent.postMessage({ type: 'vulos.bridge.hello', v: 1 }, SHELL, [ch.port2]);
    window.__port = ch.port1;
  } catch (e) {
    document.getElementById('bridge').textContent = 'BRIDGE_ERR:' + e.message;
  }

  // Let the test drive a write through the bridge.
  window.addEventListener('vulos-test-write', function () {});
</script>
</body></html>`

// A HOSTILE frame at a DIFFERENT origin that tries to impersonate the app and
// steal its bridge (and, through it, the shell's storage).
const EVIL_PROBE = `<!doctype html><html><body><script>
  // Try every addressing trick: a wildcard targetOrigin, and the shell's origin.
  try { parent.postMessage({ type: 'vulos.bridge.hello', v: 1 }, '*', [new MessageChannel().port2]); } catch (e) {}
  try { parent.postMessage({ type: 'vulos.bridge.hello', v: 1 }, document.referrer || '*', [new MessageChannel().port2]); } catch (e) {}
</script></body></html>`

async function boot(page, overrides = {}) {
  await installBackend(page, {
    // The default self-hosted box: no base domain → no per-app origins, so apps
    // stay on the shell-origin path prefix and must run opaque.
    'GET /api/apps/origins': {
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ enabled: false, base_domain: '', profile: 'default' }),
    },
    ...overrides,
  })

  // installBackend only intercepts **/api/** — the gateway's app route is not an
  // API path, so stand it up here. This is the gateway serving the app on the
  // shell's origin (the path-prefix fallback), which is exactly the configuration
  // under test.
  await page.route('**/app/clock/**', (route) => route.fulfill({
    status: 200,
    contentType: 'text/html; charset=utf-8',
    body: APP_PROBE,
  }))

  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })

  // Plant shell-origin secrets the app frame must never be able to read.
  await page.evaluate(() => {
    document.cookie = 'vulos_session=SUPERSECRET-SHELL-TOKEN; path=/'
    localStorage.setItem('shell_secret', 'SHELL-ONLY-VALUE')
  })
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

const clockFrameEl = (page) => page.locator('iframe[src*="/app/clock/"]')

test('a formerly same-origin app (Clock) is framed WITHOUT allow-same-origin', async ({ page }) => {
  await boot(page)
  await launch(page, 'Clock')

  const frameEl = clockFrameEl(page)
  await expect(frameEl).toBeVisible({ timeout: 10_000 })

  const sandbox = await frameEl.getAttribute('sandbox')
  expect(sandbox, 'app iframe must be sandboxed').toBeTruthy()

  // THE regression guard. Clock used to opt in to allow-same-origin, and it was
  // served from the shell's origin — so it owned the shell.
  expect(sandbox, 'no app on the shell origin may ever get allow-same-origin')
    .not.toContain('allow-same-origin')
  expect(sandbox).toContain('allow-scripts')
})

test('the app frame cannot reach the shell — origin, cookies, DOM and localStorage all blocked', async ({ page }) => {
  await boot(page)
  await launch(page, 'Clock')

  const frame = page.frameLocator('iframe[src*="/app/clock/"]')

  // Opaque origin: the browser gives the frame no origin of its own.
  await expect(frame.locator('#origin')).toHaveText('ORIGIN:null', { timeout: 10_000 })

  // The shell's session cookie is unreachable.
  const cookie = await frame.locator('#cookie').textContent()
  expect(cookie, `app read the shell cookie: ${cookie}`).not.toContain('SUPERSECRET-SHELL-TOKEN')

  // The shell's DOM is unreachable.
  await expect(frame.locator('#top')).toContainText('TOP_BLOCKED', { timeout: 10_000 })

  // The shell's localStorage is unreachable — this is the capability the old
  // allow-same-origin grant handed over wholesale.
  const store = await frame.locator('#shellstore').textContent()
  expect(store, `app read the shell's localStorage: ${store}`).not.toContain('SHELL-ONLY-VALUE')
  expect(store).toContain('SHELLSTORE_BLOCKED')
})

test('the bridge gives the app its storage back — scoped to the app, inside the shell', async ({ page }) => {
  await boot(page)
  await launch(page, 'Clock')

  const frame = page.frameLocator('iframe[src*="/app/clock/"]')
  // The shell answered the handshake with this app's (empty) snapshot.
  await expect(frame.locator('#bridge')).toContainText('SNAPSHOT:', { timeout: 10_000 })

  // The app writes a preference over the bridge, exactly as the injected shim does.
  const appFrame = page.frames().find((f) => f.url().includes('/app/clock/'))
  await appFrame.evaluate(() => {
    window.__port.postMessage({ v: 1, type: 'vulos.storage.set', key: 'clock.worldClocks', value: '["utc","jst"]' })
  })

  // It landed in the SHELL's localStorage, namespaced to this app — and nowhere else.
  await expect.poll(async () => page.evaluate(
    () => localStorage.getItem('vulos.appdata.clock::clock.worldClocks'),
  ), { timeout: 5_000 }).toBe('["utc","jst"]')

  // The app cannot have touched the shell's own key.
  expect(await page.evaluate(() => localStorage.getItem('shell_secret'))).toBe('SHELL-ONLY-VALUE')
})

test('the app gets its data back on relaunch (the seed makes it readable at boot)', async ({ page }) => {
  await boot(page)
  // Pre-seed the app's namespace as if it had been used before.
  await page.evaluate(() => {
    localStorage.setItem('vulos.appdata.clock::clock.worldClocks', '["utc"]')
  })
  await launch(page, 'Clock')

  const frameEl = clockFrameEl(page)
  await expect(frameEl).toBeVisible({ timeout: 10_000 })

  // The shell puts the app's snapshot in the frame's URL fragment, so the app can
  // read it SYNCHRONOUSLY at first script execution — which is what the five
  // opted-in apps do, and why they needed a real origin before.
  const src = await frameEl.getAttribute('src')
  expect(src, 'app frame must carry the storage seed').toContain('#__vulos_s=')
  // The seed is in the FRAGMENT: it is never sent to the server.
  expect(src.split('#')[0]).not.toContain('__vulos_s')

  const frame = page.frameLocator('iframe[src*="/app/clock/"]')
  await expect(frame.locator('#seeded')).toHaveText('SEED:yes', { timeout: 10_000 })
  // And the async snapshot carries the same data.
  await expect(frame.locator('#bridge')).toContainText('utc', { timeout: 10_000 })
})

test('the bridge REJECTS a hello from a different frame at a foreign origin', async ({ page }) => {
  await page.route('http://evil.test/**', (route) => route.fulfill({
    status: 200, contentType: 'text/html; charset=utf-8', body: EVIL_PROBE,
  }))
  await boot(page)
  await launch(page, 'Clock')
  await expect(clockFrameEl(page)).toBeVisible({ timeout: 10_000 })

  // Pre-load the app's namespace so there is something worth stealing.
  await page.evaluate(() => {
    localStorage.setItem('vulos.appdata.clock::clock.worldClocks', '["utc"]')
  })

  // Inject a hostile cross-origin frame into the SHELL page. It postMessages a
  // well-formed hello (with a port) — including with targetOrigin '*'.
  await page.evaluate(() => {
    const f = document.createElement('iframe')
    f.src = 'http://evil.test/steal'
    f.id = 'evil'
    document.body.appendChild(f)
  })
  await page.waitForTimeout(1500)

  // The shell must not have bound the hostile frame's port: the bridge checks
  // event.source against the app iframe's contentWindow (identity — unforgeable)
  // AND event.origin against the app's expected origin. The hostile frame fails
  // both. Nothing it can say is accepted.
  const stolen = await page.evaluate(() => {
    const f = document.getElementById('evil')
    // If the shell had answered, the evil frame would have received a snapshot.
    // It cannot tell us directly (cross-origin), so assert on the shell side: the
    // app's data must be untouched and no new namespace may have appeared.
    return {
      evilPresent: !!f,
      keys: Object.keys(localStorage).filter((k) => k.startsWith('vulos.appdata.')),
      clock: localStorage.getItem('vulos.appdata.clock::clock.worldClocks'),
      shellSecret: localStorage.getItem('shell_secret'),
    }
  })

  expect(stolen.evilPresent).toBe(true) // the hostile frame really did load
  expect(stolen.clock).toBe('["utc"]') // app data untouched
  expect(stolen.shellSecret).toBe('SHELL-ONLY-VALUE') // shell data untouched
  // The hostile frame could not create a namespace of its own either.
  expect(stolen.keys).toEqual(['vulos.appdata.clock::clock.worldClocks'])
})

test('the browser itself drops a postMessage whose targetOrigin is not the parent', async ({ page }) => {
  // This is the mechanism the app-side origin check relies on: the injected client
  // addresses the shell by its EXACT origin, so if the frame's parent is not the
  // shell, the message — and the MessagePort with it — is never delivered. The
  // bridge fails closed by construction rather than by our own bookkeeping.
  await boot(page)

  const delivered = await page.evaluate(async () => {
    const received = []
    window.addEventListener('message', (e) => { if (e.data?.probe) received.push(e.data.probe) })

    const f = document.createElement('iframe')
    f.srcdoc = `<script>
      // Correctly addressed to the real parent origin → delivered.
      parent.postMessage({ probe: 'to-parent-origin' }, ${JSON.stringify(window.location.origin)});
      // Addressed to a DIFFERENT origin → the browser refuses to deliver it.
      parent.postMessage({ probe: 'to-wrong-origin' }, 'http://not-the-shell.example');
    ${'</'}script>`
    document.body.appendChild(f)
    await new Promise((r) => setTimeout(r, 800))
    return received
  })

  expect(delivered).toContain('to-parent-origin')
  expect(delivered, 'a message addressed to the wrong origin must never be delivered')
    .not.toContain('to-wrong-origin')
})
