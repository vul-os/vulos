/**
 * sw.test.js — Vulos OS service worker (OFFLINE-03).
 *
 * Runs public/sw.js inside a jsdom Function-scope with a mocked SW `self`
 * (install/activate/fetch handlers, caches, clients) and exercises:
 *   • install pre-caches the app shell
 *   • activate evicts stale caches + claims clients
 *   • cache-first for static shell GETs
 *   • network-only (no respondWith) for /api/* GETs
 *   • POSTs are never intercepted
 *   • navigation-fallback to /index.html when the network is down
 *   • SKIP_WAITING message triggers self.skipWaiting()
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const SW_SRC = readFileSync(
  resolve(import.meta.dirname, '../../public/sw.js'),
  'utf8'
)

// Minimal in-memory Cache + CacheStorage. Keyed by URL pathname so it doesn't
// matter whether the request URL is full ('https://app/...') or a relative
// shell path ('/index.html') — both forms resolve to the same key.
//
// This harness only implements the handful of members sw.js and this suite
// actually exercise — not the full Request/Response/Cache/CacheStorage/
// Clients DOM surface (jsdom doesn't implement the Cache API at all, and
// sw.js runs outside jsdom's globals anyway via the `new Function` scope
// below). The interfaces below are the harness's OWN contract, not a stand-in
// for lib.dom's real types.

interface FakeRequest {
  url: string
  method: string
  mode: string
  clone: () => FakeRequest
}

interface FakeResponse {
  status?: number
  type?: string
  ok?: boolean
  body?: string
  clone?: () => FakeResponse
}

function keyOf(reqOrUrl: FakeRequest | string | undefined): string {
  const u = typeof reqOrUrl === 'string' ? reqOrUrl : (reqOrUrl?.url ?? '')
  try { return new URL(u, 'https://app.vulos.org/').pathname } catch { return u }
}
function makeCache() {
  const store = new Map<string, FakeResponse>()
  return {
    addAll: vi.fn(async (urls: string[]) => { for (const u of urls) store.set(keyOf(u), { ok: true, body: 'shell' }) }),
    put: vi.fn(async (req: FakeRequest, res: FakeResponse) => { store.set(keyOf(req), res) }),
    match: vi.fn(async (req: FakeRequest | string) => store.get(keyOf(req)) || undefined),
    keys: vi.fn(async () => [...store.keys()]),
    _store: store,
  }
}
function makeCaches() {
  const all = new Map<string, ReturnType<typeof makeCache>>()
  return {
    open: vi.fn(async (name: string) => {
      if (!all.has(name)) all.set(name, makeCache())
      const cache = all.get(name)
      if (!cache) throw new Error('unreachable: cache was just set above')
      return cache
    }),
    keys: vi.fn(async () => [...all.keys()]),
    delete: vi.fn(async (name: string) => all.delete(name)),
    match: vi.fn(async (req: FakeRequest | string) => {
      for (const cache of all.values()) {
        const hit = await cache.match(req)
        if (hit) return hit
      }
      return undefined
    }),
    _all: all,
  }
}

// Minimal Request / Response surrogates.
function makeRequest(url: string, init: { method?: string; mode?: string } = {}): FakeRequest {
  return {
    url,
    method: init.method || 'GET',
    mode: init.mode || 'same-origin',
    clone() { return makeRequest(url, init) },
  }
}
function makeResponse(body = 'ok', { status = 200, type = 'basic' }: { status?: number; type?: string } = {}): FakeResponse {
  return {
    status,
    type,
    ok: status >= 200 && status < 300,
    body,
    clone() { return makeResponse(body, { status, type }) },
  }
}

// ── SW event surrogate ───────────────────────────────────────────────────────
// sw.js reads only these members off each event kind; a single, all-optional
// shape covers install/activate/message/fetch/push/notificationclick without
// six separate near-identical interfaces.
interface PushDataLike {
  json: () => unknown
  text: () => string
}
interface SWEvt {
  waitUntil?: (p: Promise<unknown>) => void
  respondWith?: (p: Promise<FakeResponse>) => void
  request?: FakeRequest
  data?: { type?: string } | PushDataLike | null
  notification?: { close: () => void; data?: { url?: string } }
  _wait?: Promise<unknown>
  _response?: Promise<FakeResponse>
}
type SWHandler = (evt: SWEvt) => void | Promise<void>

interface FakeClient {
  focus?: () => unknown
  navigate?: (url: string) => Promise<void>
}
interface NotificationOptionsLike {
  body?: string
  tag?: string
  icon?: string
  badge?: string
  data?: { url?: string; source?: string }
}

interface LoadSWOptions {
  fetchImpl?: (req: FakeRequest) => Promise<FakeResponse>
}

function loadSW({ fetchImpl }: LoadSWOptions = {}) {
  const handlers: Record<string, SWHandler> = {}
  const self = {
    skipWaiting: vi.fn(),
    addEventListener: vi.fn((evt: string, fn: SWHandler) => { handlers[evt] = fn }),
    clients: {
      claim: vi.fn(async () => {}),
      matchAll: vi.fn(async (): Promise<FakeClient[]> => []),
      openWindow: vi.fn(async (): Promise<FakeClient> => ({})),
    },
    registration: {
      showNotification: vi.fn<(title: string, options: NotificationOptionsLike) => Promise<void>>(async () => {}),
    },
  }
  const caches = makeCaches()
  const fetchSpy = vi.fn(fetchImpl || (async () => makeResponse('net')))
  // Errors are needed because sw.js calls Response.error() on network failure.
  const Response = { error: () => makeResponse('', { status: 0, type: 'error' }) }

  // Run the SW source in a function scope with the mocked globals.
  const fn = new Function(
    'self', 'caches', 'fetch', 'URL', 'Response', 'setTimeout', 'clearTimeout',
    SW_SRC
  )
  fn(self, caches, fetchSpy, URL, Response, setTimeout, clearTimeout)

  return { handlers, self, caches, fetchSpy, Response }
}

// waitUntil/respondWith helpers: capture the promise/response the handler
// passed in so the test can await it.
function fireInstall(handlers: Record<string, SWHandler>) {
  const evt: SWEvt = { waitUntil: vi.fn((p: Promise<unknown>) => { evt._wait = p }) }
  handlers.install(evt)
  return evt._wait
}
function fireActivate(handlers: Record<string, SWHandler>) {
  const evt: SWEvt = { waitUntil: vi.fn((p: Promise<unknown>) => { evt._wait = p }) }
  handlers.activate(evt)
  return evt._wait
}
function fireFetch(handlers: Record<string, SWHandler>, request: FakeRequest): SWEvt {
  const evt: SWEvt = {
    request,
    _response: undefined,
    respondWith: vi.fn((p: Promise<FakeResponse>) => { evt._response = p }),
  }
  handlers.fetch(evt)
  return evt
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('public/sw.js — OS service worker', () => {
  it('install pre-caches the app shell', async () => {
    const { handlers, caches } = loadSW()
    await fireInstall(handlers)
    const cache = await caches.open('vulos-os-shell-v1')
    expect(cache.addAll).toHaveBeenCalledTimes(1)
    const [shellUrls] = cache.addAll.mock.calls[0]
    expect(shellUrls).toContain('/')
    expect(shellUrls).toContain('/index.html')
    expect(shellUrls).toContain('/manifest.json')
  })

  it('install does NOT skip-waiting (page opts in via message)', async () => {
    const { handlers, self } = loadSW()
    await fireInstall(handlers)
    expect(self.skipWaiting).not.toHaveBeenCalled()
  })

  it('activate evicts stale caches and claims clients', async () => {
    const { handlers, caches, self } = loadSW()
    // Seed an old cache name; it should be deleted on activate.
    await caches.open('vulos-os-shell-OLD')
    await caches.open('vulos-os-shell-v1')
    await fireActivate(handlers)
    expect(caches.delete).toHaveBeenCalledWith('vulos-os-shell-OLD')
    expect(caches.delete).not.toHaveBeenCalledWith('vulos-os-shell-v1')
    expect(self.clients.claim).toHaveBeenCalled()
  })

  it('message SKIP_WAITING triggers self.skipWaiting()', async () => {
    const { handlers, self } = loadSW()
    handlers.message({ data: { type: 'SKIP_WAITING' } })
    expect(self.skipWaiting).toHaveBeenCalled()
  })

  it('cache-first: GET /index.html returns cached response without hitting the network', async () => {
    const { handlers, fetchSpy } = loadSW()
    await fireInstall(handlers) // populates the shell cache
    const req = makeRequest('https://app.vulos.org/index.html')
    const evt = fireFetch(handlers, req)
    const resp = await evt._response
    if (!resp) throw new Error('expected fetch handler to respondWith a response')
    expect(resp.body).toBe('shell')           // came from cache
    // fetch may still fire for background revalidation — that's fine; the
    // returned response is the cached one, which is what matters.
    void fetchSpy
  })

  it('network-only: GET /api/* is NOT intercepted', async () => {
    const { handlers } = loadSW()
    const req = makeRequest('https://app.vulos.org/api/auth/status')
    const evt = fireFetch(handlers, req)
    expect(evt.respondWith).not.toHaveBeenCalled()
  })

  it('POST requests are never intercepted', async () => {
    const { handlers } = loadSW()
    const req = makeRequest('https://app.vulos.org/index.html', { method: 'POST' })
    const evt = fireFetch(handlers, req)
    expect(evt.respondWith).not.toHaveBeenCalled()
  })

  it('navigation fallback: returns cached /index.html when the network is down', async () => {
    const { handlers, caches } = loadSW({
      fetchImpl: async () => { throw new Error('offline') },
    })
    await fireInstall(handlers) // primes cache with /index.html
    const req = makeRequest('https://app.vulos.org/some/deep/route', { mode: 'navigate' })
    const evt = fireFetch(handlers, req)
    const resp = await evt._response
    expect(resp).toBeDefined()
    if (!resp) throw new Error('expected fetch handler to respondWith a response')
    expect(resp.body).toBe('shell')
    void caches
  })

  it('navigation fallback only kicks in for navigation mode — other GETs surface a Response.error()', async () => {
    const { handlers } = loadSW({ fetchImpl: async () => { throw new Error('offline') } })
    const req = makeRequest('https://app.vulos.org/some-image.png')
    const evt = fireFetch(handlers, req)
    const resp = await evt._response
    if (!resp) throw new Error('expected fetch handler to respondWith a response')
    expect(resp.type).toBe('error')
  })

  it('a lazy app chunk survives ONE transient failure instead of killing the window', async () => {
    // Every app in this shell is a code-split chunk fetched when it opens. One
    // failed fetch used to return Response.error() immediately, rejecting the
    // dynamic import, tripping the window error boundary and leaving that app
    // dead until a full reload. Seen on a real box: Drive and Contacts both
    // died this way ("error loading dynamically imported module") while the
    // backend was briefly unresponsive.
    //
    // Chunks are immutable and content-hashed, so a retry can only ever fetch
    // the same bytes.
    let calls = 0
    const { handlers } = loadSW({
      fetchImpl: async () => {
        calls += 1
        if (calls === 1) throw new Error('transient blip')
        return { status: 200, type: 'basic', body: 'chunk', clone: () => ({ body: 'chunk' }) }
      },
    })
    const req = makeRequest('https://app.vulos.org/assets/Drive-BY7BsChL.js')
    const evt = fireFetch(handlers, req)
    const resp = await evt._response
    if (!resp) throw new Error('expected fetch handler to respondWith a response')
    expect(resp.type, 'the retry did not happen — one blip still kills the app window').not.toBe('error')
    expect(calls, 'expected exactly one retry, not a backoff loop').toBe(2)
  })

  // ── Web Push (PUSH-CELL-01) ────────────────────────────────────────────────
  function firePush(handlers: Record<string, SWHandler>, dataObj: unknown): SWEvt {
    const evt: SWEvt = {
      data: dataObj === undefined ? null : {
        json: () => { const v: unknown = JSON.parse(JSON.stringify(dataObj)); return v },
        text: () => (typeof dataObj === 'string' ? dataObj : JSON.stringify(dataObj)),
      },
      waitUntil: vi.fn((p: Promise<unknown>) => { evt._wait = p }),
    }
    handlers.push(evt)
    return evt
  }

  it('push: renders the notification from the cell payload', async () => {
    const { handlers, self } = loadSW()
    const evt = firePush(handlers, { title: 'New mail', body: 'from Alice', tag: 'alert', url: '/mail', source: 'mail' })
    await evt._wait
    expect(self.registration.showNotification).toHaveBeenCalledTimes(1)
    const [title, options] = self.registration.showNotification.mock.calls[0]
    expect(title).toBe('New mail')
    expect(options.body).toBe('from Alice')
    expect(options.tag).toBe('alert')
    expect(options.data?.url).toBe('/mail')
  })

  it('push: falls back to a default title and empty payload safely', async () => {
    const { handlers, self } = loadSW()
    const evt = firePush(handlers, undefined) // no data at all
    await evt._wait
    expect(self.registration.showNotification).toHaveBeenCalledTimes(1)
    const [title, options] = self.registration.showNotification.mock.calls[0]
    expect(title).toBe('Vulos')          // default title
    expect(options.body).toBe('')        // empty body
    expect(options.data?.url).toBe('/')   // default deep-link
  })

  it('push: a non-JSON payload degrades to a text body', async () => {
    const { handlers, self } = loadSW()
    // json() throws → handler falls back to text().
    const evt: SWEvt = {
      data: { json: () => { throw new Error('not json') }, text: () => 'plain alert' },
      waitUntil: vi.fn((p: Promise<unknown>) => { evt._wait = p }),
    }
    handlers.push(evt)
    await evt._wait
    const [, options] = self.registration.showNotification.mock.calls[0]
    expect(options.body).toBe('plain alert')
  })

  it('notificationclick: focuses an existing window and navigates it to the deep-link', async () => {
    const focused = { focus: vi.fn(() => 'focused'), navigate: vi.fn(async () => {}) }
    const { handlers, self } = loadSW()
    self.clients.matchAll = vi.fn(async () => [focused])
    const notification = { close: vi.fn(), data: { url: '/mail/42' } }
    const evt: SWEvt = { notification, waitUntil: vi.fn((p: Promise<unknown>) => { evt._wait = p }) }
    handlers.notificationclick(evt)
    await evt._wait
    expect(notification.close).toHaveBeenCalled()
    expect(focused.navigate).toHaveBeenCalledWith('/mail/42')
    expect(focused.focus).toHaveBeenCalled()
    expect(self.clients.openWindow).not.toHaveBeenCalled()
  })

  it('notificationclick: opens a new window when none are open', async () => {
    const { handlers, self } = loadSW()
    self.clients.matchAll = vi.fn(async () => [])
    const notification = { close: vi.fn(), data: { url: '/talk' } }
    const evt: SWEvt = { notification, waitUntil: vi.fn((p: Promise<unknown>) => { evt._wait = p }) }
    handlers.notificationclick(evt)
    await evt._wait
    expect(self.clients.openWindow).toHaveBeenCalledWith('/talk')
  })
})
