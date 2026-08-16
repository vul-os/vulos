// What URL does a bundled app's API call actually go to?
//
// WHY THIS TEST EXISTS
// --------------------
// A parallel agent booted a real image and found System Info rendering every
// panel empty while `/app/system-info/api/info` answered correctly through the
// gateway. The app was asking the wrong place: `frontend/apps/*/index.html`
// fetched absolute `/api/…`, and with per-app origins DISABLED — the default in
// src/core/AppOrigins.ts — the app is served at `/app/{id}/` on the SHELL's
// origin, so an absolute path leaves the app's mount entirely.
//
// That defect is invisible to every other spec in this directory, because they
// mock the backend with `page.route('**/api/**')` and a mock answers whatever
// path it is asked. Nothing mocked here: a real server records what the browser
// really put on the wire, and the assertions are about the recorded path.
//
// WHAT IS REAL AND WHAT IS A STAND-IN
// -----------------------------------
// REAL: `apps/_shared/vulos-api.js` is read off disk and inlined into the frame
//   exactly as `sync-api-helper.mjs` inlines it into the shipping apps.
// REAL: the frame URL and the sandbox attribute come from the shell's own
//   `resolveAppFrameURL` / `iframeSandboxForURL` in src/core/AppOrigins.ts, with
//   the two origin configurations a box can actually be in. Nothing about the
//   two modes is hardcoded here.
// REAL: the browser. Both frames run in Chromium with the shell's real sandbox,
//   so `Origin` and `Cookie` on the recorded requests are what a box would see.
// STAND-IN: the server. `ip netns`, the app namespace and the Python app
//   servers are Linux-only, so a Node server plays the gateway — it strips
//   `/app/{id}` exactly as `gateway.go` Handler does (appPath = r.URL.Path on an
//   app origin, the remainder after the prefix otherwise) and records every
//   request. What that cannot prove is the netns hop and the app's own
//   server.py; what it does prove is the only thing in dispute — which URL the
//   browser requests, in each of the two modes.
//
// NO SHARED PORT. This spec never touches the suite's preview server or its
// baseURL; it binds an ephemeral port of its own, so it cannot be handed
// another agent's build the way the marketing-site incident handed one to the
// wrong app.

import { test, expect } from '@playwright/test'
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { resolveAppFrameURL, iframeSandboxForURL, appFrameURLFor } from '../src/core/AppOrigins'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const HELPER = path.join(HERE, '..', 'apps', '_shared', 'vulos-api.js')

const APP_ID = 'gallery'
const PROFILE = 'default'

interface Hit {
  host: string
  url: string
  origin: string
  cookie: string
}

/** Mirrors gateway.go Handler(): on an app origin the app sees the whole path; on
 *  the path prefix it sees what is left after `/app/{id}` is removed. */
function appPathOf(rawUrl: string, onAppOrigin: boolean): string {
  if (onAppOrigin) return rawUrl
  const rest = rawUrl.startsWith('/app/') ? rawUrl.slice('/app/'.length) : rawUrl
  const slash = rest.indexOf('/')
  return slash === -1 ? '/' : rest.slice(slash)
}

let server: http.Server
let port = 0
let hits: Hit[] = []

/** The frame document: the real shared resolver, then two probes — one through
 *  the resolver and one absolute, so a single page records both the fix and the
 *  defect side by side. */
function frameHtml(): string {
  const helper = fs.readFileSync(HELPER, 'utf8')
  if (!helper.includes('function appUrl')) {
    throw new Error(`${HELPER} does not define appUrl; this test would prove nothing`)
  }
  return `<!doctype html><meta charset="utf-8"><title>frame</title>
<script data-vulos-shared="vulos-api.js">
${helper}</script>
<script>
  var base = encodeURIComponent(vulosApi.mountBase());
  var doc  = encodeURIComponent(document.baseURI);
  // The shipping call shape, e.g. gallery/index.html line 149.
  fetch(vulosApi.appUrl('/api/probe?via=resolver&base=' + base + '&doc=' + doc)).catch(function(){});
  // The shape every bundled app used before this change.
  fetch('/api/probe?via=absolute').catch(function(){});
</script>
<body>frame up</body>`
}

test.beforeAll(async () => {
  server = http.createServer((req, res) => {
    const host = req.headers.host || ''
    const onAppOrigin = host.startsWith(`${APP_ID}--${PROFILE}.`)
    const raw = req.url || '/'
    hits.push({
      host,
      url: raw,
      origin: String(req.headers.origin ?? '<absent>'),
      cookie: String(req.headers.cookie ?? '<absent>'),
    })
    const appPath = appPathOf(raw, onAppOrigin)
    if (appPath === '/' || appPath.startsWith('/?')) {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
      res.end(frameHtml())
      return
    }
    if (appPath.startsWith('/shell')) {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
      res.end('<!doctype html><meta charset="utf-8"><title>shell</title><body></body>')
      return
    }
    if (appPath.startsWith('/api/probe')) {
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end('{"ok":true}')
      return
    }
    res.writeHead(404, { 'content-type': 'application/json' })
    res.end('{"error":"not found"}')
  })
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
  port = (server.address() as { port: number }).port
})

test.afterAll(async () => {
  await new Promise<void>((r) => server.close(() => r()))
})

test.beforeEach(() => {
  hits = []
})

/** Frames `url` with the shell's real sandbox and waits for the two probes. */
async function runFrame(page: import('@playwright/test').Page, shellHost: string, url: string, sandbox: string) {
  await page.goto(`http://${shellHost}:${port}/shell`)
  await page.evaluate(
    ([src, sbx]) => {
      const f = document.createElement('iframe')
      f.setAttribute('sandbox', sbx)
      f.src = src
      document.body.appendChild(f)
    },
    [url, sandbox] as const,
  )
  await expect
    .poll(() => hits.filter((h) => h.url.includes('/api/probe')).length, { timeout: 10_000 })
    .toBeGreaterThanOrEqual(2)
}

const probe = (via: string) => hits.find((h) => h.url.includes(`via=${via}`))

test('origins DISABLED (the default): the resolver keeps the call inside /app/{id}/, an absolute path does not', async ({
  page,
}) => {
  // No base domain configured — exactly what AppOrigins falls back to when
  // GET /api/apps/origins is absent, and what setOriginConfig produces for
  // {"enabled":true} with no base_domain.
  const cfg = { enabled: false, baseDomain: '', profile: PROFILE }
  const loc = { hostname: 'localhost', port: String(port), protocol: 'http:' }

  const frameUrl = resolveAppFrameURL(`/app/${APP_ID}/`, cfg, loc)
  expect(frameUrl, 'with origins off the shell frames the gateway path prefix').toBe(`/app/${APP_ID}/`)
  expect(appFrameURLFor(APP_ID, cfg, loc)).toBe(`/app/${APP_ID}/`)

  const sandbox = iframeSandboxForURL(frameUrl)
  expect(sandbox, 'a shell-origin frame must not get allow-same-origin').not.toContain('allow-same-origin')

  await runFrame(page, 'localhost', `http://localhost:${port}${frameUrl}`, sandbox)

  const resolved = probe('resolver')
  const absolute = probe('absolute')
  expect(resolved, 'the resolver probe never reached the server').toBeTruthy()
  expect(absolute, 'the absolute probe never reached the server').toBeTruthy()

  // THE FIX: the resolved call stays under the app's mount, so gateway.go
  // Handler() strips /app/gallery and the app's own server sees /api/probe.
  expect(resolved!.url).toMatch(new RegExp(`^/app/${APP_ID}/api/probe\\?`))
  expect(appPathOf(resolved!.url, false)).toMatch(/^\/api\/probe\?/)
  expect(new URL(resolved!.url, 'http://x').searchParams.get('base')).toBe(`/app/${APP_ID}/`)

  // THE DEFECT: the absolute call never entered the app route at all. It is not
  // "the app's API on a different path" — it is the SHELL's origin root, where
  // /api/ai/chat, /api/files, /api/proxy/ and /api/telephony/ are real,
  // unrelated endpoints.
  expect(absolute!.url).toBe('/api/probe?via=absolute')
  expect(absolute!.url.startsWith(`/app/${APP_ID}/`)).toBe(false)
})

test('origins ENABLED: the same resolver call lands at the app origin root', async ({ page }) => {
  // Chromium resolves every *.localhost name to loopback (RFC 6761), so the
  // minted origin is reachable without touching /etc/hosts or DNS.
  const cfg = { enabled: true, baseDomain: 'localhost', profile: PROFILE }
  const loc = { hostname: 'localhost', port: String(port), protocol: 'http:' }

  const frameUrl = resolveAppFrameURL(`/app/${APP_ID}/`, cfg, loc)
  expect(frameUrl, 'with origins on the shell rehomes the app onto its own origin').toBe(
    `http://${APP_ID}--${PROFILE}.localhost:${port}/`,
  )

  const sandbox = iframeSandboxForURL(frameUrl)
  expect(sandbox, 'a distinct origin is exactly when allow-same-origin is granted').toContain('allow-same-origin')

  await runFrame(page, 'localhost', frameUrl, sandbox)

  const resolved = probe('resolver')
  expect(resolved, 'the resolver probe never reached the server').toBeTruthy()
  expect(resolved!.host).toBe(`${APP_ID}--${PROFILE}.localhost:${port}`)
  // At the app origin the app IS the root, so the resolver must add nothing.
  expect(resolved!.url).toMatch(/^\/api\/probe\?/)
  expect(new URL(resolved!.url, 'http://x').searchParams.get('base')).toBe('/')

  // A fix that only worked with origins disabled would have moved the bug; the
  // absolute probe happens to be right here, and the resolved one agrees with it.
  const absolute = probe('absolute')
  expect(absolute!.host).toBe(`${APP_ID}--${PROFILE}.localhost:${port}`)
  expect(absolute!.url).toBe('/api/probe?via=absolute')
})

test('what the absolute path was actually reaching: the shell origin, with no credentials', async ({
  page,
  context,
}) => {
  // Whether the old absolute calls were merely BROKEN or were SILENTLY TALKING
  // TO THE SHELL'S API AS THE USER decides how bad the defect was, so it is
  // measured rather than argued.
  await context.addCookies([
    { name: 'vulos_session', value: 'shell-session-token', domain: 'localhost', path: '/' },
  ])

  const cfg = { enabled: false, baseDomain: '', profile: PROFILE }
  const frameUrl = `/app/${APP_ID}/`
  const sandbox = iframeSandboxForURL(resolveAppFrameURL(frameUrl, cfg))
  await runFrame(page, 'localhost', `http://localhost:${port}${frameUrl}`, sandbox)

  const nav = hits.find((h) => h.url === `/app/${APP_ID}/`)
  expect(nav, 'the frame document itself was never fetched').toBeTruthy()
  // The frame NAVIGATION is same-site, so the box's session cookie rides along
  // and the document loads. That is why the app appears — and then fails.
  expect(nav!.cookie).toContain('vulos_session=shell-session-token')

  const absolute = probe('absolute')!
  const resolved = probe('resolver')!

  // The sandbox denies allow-same-origin, so the document runs in an opaque
  // origin: every fetch it makes is a CORS request stamped `Origin: null`, and
  // fetch's default credentials mode ('same-origin') therefore sends nothing.
  expect(absolute.origin).toBe('null')
  expect(absolute.cookie).toBe('<absent>')
  expect(resolved.origin).toBe('null')
  expect(resolved.cookie).toBe('<absent>')

  // So the absolute calls were NOT quietly acting as the user against the
  // shell's API — they arrived unauthenticated at a wrong endpoint. That is the
  // better of the two outcomes, and it holds only because AppOrigins withholds
  // allow-same-origin here (the SANDBOX-01/02 grant it removed would have made
  // these same requests same-origin and fully credentialed). This assertion is
  // the tripwire on that: re-granting allow-same-origin turns the line above
  // into a credentialed cross-app request and fails here.
  expect(sandbox).not.toContain('allow-same-origin')
})
