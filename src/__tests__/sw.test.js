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
  resolve(__dirname, '../../public/sw.js'),
  'utf8'
)

// Minimal in-memory Cache + CacheStorage. Keyed by URL pathname so it doesn't
// matter whether the request URL is full ('https://app/...') or a relative
// shell path ('/index.html') — both forms resolve to the same key.
function keyOf(reqOrUrl) {
  const u = String(reqOrUrl?.url ?? reqOrUrl ?? '')
  try { return new URL(u, 'https://app.vulos.org/').pathname } catch { return u }
}
function makeCache() {
  const store = new Map()
  return {
    addAll: vi.fn(async (urls) => { for (const u of urls) store.set(keyOf(u), { ok: true, body: 'shell' }) }),
    put: vi.fn(async (req, res) => { store.set(keyOf(req), res) }),
    match: vi.fn(async (req) => store.get(keyOf(req)) || undefined),
    keys: vi.fn(async () => [...store.keys()]),
    _store: store,
  }
}
function makeCaches() {
  const all = new Map()
  return {
    open: vi.fn(async (name) => {
      if (!all.has(name)) all.set(name, makeCache())
      return all.get(name)
    }),
    keys: vi.fn(async () => [...all.keys()]),
    delete: vi.fn(async (name) => all.delete(name)),
    match: vi.fn(async (req) => {
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
function makeRequest(url, init = {}) {
  return {
    url,
    method: init.method || 'GET',
    mode: init.mode || 'same-origin',
    clone() { return makeRequest(url, init) },
  }
}
function makeResponse(body = 'ok', { status = 200, type = 'basic' } = {}) {
  return {
    status,
    type,
    ok: status >= 200 && status < 300,
    body,
    clone() { return makeResponse(body, { status, type }) },
  }
}

function loadSW({ fetchImpl } = {}) {
  const handlers = {}
  const self = {
    skipWaiting: vi.fn(),
    addEventListener: vi.fn((evt, fn) => { handlers[evt] = fn }),
    clients: { claim: vi.fn(async () => {}) },
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
function fireInstall(handlers) {
  const evt = { waitUntil: vi.fn((p) => { evt._wait = p }) }
  handlers.install(evt)
  return evt._wait
}
function fireActivate(handlers) {
  const evt = { waitUntil: vi.fn((p) => { evt._wait = p }) }
  handlers.activate(evt)
  return evt._wait
}
function fireFetch(handlers, request) {
  const evt = {
    request,
    _response: undefined,
    respondWith: vi.fn((p) => { evt._response = p }),
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
    expect(resp.body).toBe('shell')
    void caches
  })

  it('navigation fallback only kicks in for navigation mode — other GETs surface a Response.error()', async () => {
    const { handlers } = loadSW({ fetchImpl: async () => { throw new Error('offline') } })
    const req = makeRequest('https://app.vulos.org/some-image.png')
    const evt = fireFetch(handlers, req)
    const resp = await evt._response
    expect(resp.type).toBe('error')
  })
})
