/**
 * endpoints.test.js — cloud↔LAN failover (OS OFFLINE-02 contract).
 *
 * RELAY-CLIENT-04: the implementation lives in src/lib/net/endpoints.js
 * now; this suite still owns the OS-specific guarantee that the shared
 * `configure({ lsKeyPrefix: 'vulos.os.endpoints.v1' })` migration seam keeps
 * existing OS user state intact (the cache key must NOT change — that would
 * wipe every OS user's last-known-good cloud↔LAN pair on first post-migration
 * load and force an unnecessary re-probe round-trip).
 *
 * Covers the frozen contract:
 *   • both endpoints cached under 'vulos.os.endpoints.v1'
 *   • reachable chosen automatically
 *   • cloud-down → LAN
 *   • LAN-down → cloud
 *   • prefer LAN-direct when both are reachable (latency)
 *   • 401/403 counts as reachable (box is up)
 *   • invalidation re-probes on next selectEndpoint call
 *   • re-selects on the window 'online' event
 *   • cached pair survives a "reload" (fresh module, no injection/env)
 *   • seedFromResolveBackend() persists a fresh BackendTarget
 *   • no-LAN response (cloud-only target) still works
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

const CLOUD = 'https://box.vulos.org'
const LAN = 'https://box.abc.lan.vulos.org'

// Each test gets a fresh module instance so internal selection state is reset.
// The freshly-imported module re-runs configure() with the OS lsKeyPrefix so
// the cached pair lives under 'vulos.os.endpoints.v1' (the pre-migration key,
// preserved verbatim for backwards-compat).
async function freshModule() {
  vi.resetModules()
  const mod = await import('../lib/net/endpoints.js')
  mod.configure({ lsKeyPrefix: 'vulos.os.endpoints.v1', healthPath: '/api/auth/status' })
  return mod
}

function setEndpoints({ cloud = CLOUD, lan = LAN } = {}) {
  globalThis.window = globalThis.window || {}
  window.__VULOS_ENDPOINTS__ = { cloud, lan }
}

// The cache key itself is the OS-migration contract this suite guards — read
// it back the same way the app does, failing loudly (not silently) if a test
// asserts on a pair that was never persisted.
function getCachedPair() {
  const raw = localStorage.getItem('vulos.os.endpoints.v1')
  if (!raw) throw new Error('expected a cached endpoints pair in localStorage')
  return JSON.parse(raw)
}

beforeEach(() => {
  // jsdom provides localStorage; clear it so cached pairs don't leak across tests.
  try { localStorage.clear() } catch { /* ignore */ }
  globalThis.window = globalThis.window || {}
  if (!window.addEventListener) window.addEventListener = () => {}
  globalThis.navigator = globalThis.navigator || {}
})

afterEach(() => {
  vi.restoreAllMocks()
  delete window.__VULOS_ENDPOINTS__
})

describe('endpoint failover', () => {
  it('caches BOTH cloud + LAN endpoints', async () => {
    setEndpoints()
    const ep = await freshModule()
    const pair = ep.resolveEndpoints()
    expect(pair.cloud).toBe(CLOUD)
    expect(pair.lan).toBe(LAN)
    // Persisted so a later offline load still has both to fail over between.
    const cached = getCachedPair()
    expect(cached.cloud).toBe(CLOUD)
    expect(cached.lan).toBe(LAN)
  })

  it('prefers LAN-direct when both are reachable', async () => {
    setEndpoints()
    const ep = await freshModule()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))
    const selected = await ep.selectEndpoint({ force: true })
    expect(selected).toBe(LAN)
  })

  it('falls back to cloud when LAN is down', async () => {
    setEndpoints()
    const ep = await freshModule()
    vi.stubGlobal('fetch', vi.fn(async (url) => {
      if (String(url).startsWith(LAN)) throw new Error('LAN unreachable')
      return { ok: true, status: 200 }
    }))
    const selected = await ep.selectEndpoint({ force: true })
    expect(selected).toBe(CLOUD)
  })

  it('falls back to LAN when cloud is down', async () => {
    setEndpoints()
    const ep = await freshModule()
    vi.stubGlobal('fetch', vi.fn(async (url) => {
      if (String(url).startsWith(CLOUD)) throw new Error('cloud route down')
      return { ok: true, status: 200 }
    }))
    const selected = await ep.selectEndpoint({ force: true })
    expect(selected).toBe(LAN)
  })

  it('falls back to same-origin when both remote endpoints are down', async () => {
    setEndpoints()
    const ep = await freshModule()
    // navigator.onLine is a read-only getter in jsdom; override it for the probe.
    Object.defineProperty(navigator, 'onLine', { value: false, configurable: true })
    vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('no network') }))
    const selected = await ep.selectEndpoint({ force: true })
    expect(selected).toBe('')
  })

  it('counts a 401/403 as reachable (the box is up)', async () => {
    setEndpoints({ cloud: '', lan: LAN })
    const ep = await freshModule()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 401 })))
    const selected = await ep.selectEndpoint({ force: true })
    expect(selected).toBe(LAN)
  })

  it('invalidateEndpoint forces a re-probe on the next selectEndpoint call', async () => {
    setEndpoints()
    const ep = await freshModule()
    const fetchMock = vi.fn(async () => ({ ok: true, status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await ep.selectEndpoint({ force: true })
    const firstCalls = fetchMock.mock.calls.length
    expect(firstCalls).toBeGreaterThan(0)

    // Without invalidation: cached, no extra probe within REVALIDATE_AFTER_MS.
    await ep.selectEndpoint()
    expect(fetchMock.mock.calls.length).toBe(firstCalls)

    // After invalidate: next call re-probes.
    ep.invalidateEndpoint()
    await ep.selectEndpoint()
    expect(fetchMock.mock.calls.length).toBeGreaterThan(firstCalls)
  })

  it('re-selects on the window "online" event', async () => {
    setEndpoints()
    // A ref object (not a bare `let`) — TS's narrowing otherwise pins a `let`
    // reassigned only inside a nested closure to its initial `null` literal at
    // every read site in this outer scope, regardless of the intervening
    // freshModule() call that actually invokes the closure and mutates it.
    const handlerRef: { current: (() => void) | null } = { current: null }
    // endpoints.ts registers a zero-arg listener (`window.addEventListener('online', () => {...})`),
    // so the mock matches that real call shape. Installed via defineProperty
    // (rather than a direct `window.addEventListener =` assignment) because
    // the DOM lib's addEventListener is overloaded — a plain assignment
    // demands the mock satisfy every overload, which a narrower, honestly
    // -typed stand-in for our own zero-arg listener never will.
    const addEventListener = vi.fn((evt: string, fn: () => void) => {
      if (evt === 'online') handlerRef.current = fn
    })
    Object.defineProperty(window, 'addEventListener', { value: addEventListener, writable: true, configurable: true })

    const ep = await freshModule()
    expect(typeof handlerRef.current).toBe('function')

    const fetchMock = vi.fn(async () => ({ ok: true, status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    // Fire the online event — handler should force a fresh selection.
    handlerRef.current?.()
    // Deterministically wait for the async selectEndpoint chain to reach the
    // probe fetch. A fixed number of microtask yields is flaky — the chain has a
    // variable number of awaits before fetch, which is the pre-existing flake.
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const selected = await ep.selectEndpoint()
    expect(selected).toBe(LAN)
  })

  it('cached pair survives a "reload" — no injection or env, only localStorage', async () => {
    // First load: inject + persist a known pair.
    setEndpoints()
    {
      const ep = await freshModule()
      ep.resolveEndpoints()
    }
    // Second load: drop the injected globals — only the localStorage cache
    // should survive, and the pair must still be available to fail over between.
    delete window.__VULOS_ENDPOINTS__
    const ep = await freshModule()
    const pair = ep.resolveEndpoints()
    expect(pair.cloud).toBe(CLOUD)
    expect(pair.lan).toBe(LAN)
  })

  it('seedFromResolveBackend persists a fresh BackendTarget', async () => {
    const ep = await freshModule()
    const target = {
      Endpoint: CLOUD,
      LANCandidate: { BoxID: 'abc', Endpoint: LAN },
    }
    const pair = ep.seedFromResolveBackend(target)
    expect(pair.cloud).toBe(CLOUD)
    expect(pair.lan).toBe(LAN)
    const cached = getCachedPair()
    expect(cached.cloud).toBe(CLOUD)
    expect(cached.lan).toBe(LAN)
  })

  it('handles a BackendTarget with no LANCandidate (cloud-only)', async () => {
    const ep = await freshModule()
    const target = { Endpoint: CLOUD, LANCandidate: null }
    const pair = ep.seedFromResolveBackend(target)
    expect(pair.cloud).toBe(CLOUD)
    expect(pair.lan).toBe('')

    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))
    const selected = await ep.selectEndpoint({ force: true })
    // With no LAN candidate cloud is the highest-priority reachable endpoint.
    expect(selected).toBe(CLOUD)
  })
})
