/**
 * offlineBootstrap.test.js — OS offline bootstrap (OFFLINE-03).
 *
 * Covers:
 *   • bootstrapOffline registers the SW exactly once even if called twice
 *   • bootstrapOffline primes selectEndpoint()
 *   • onUpdateAvailable fires when the SW transitions installing → installed
 *     with an existing controller
 *   • applyUpdate posts SKIP_WAITING to the waiting worker
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

async function freshModule() {
  vi.resetModules()
  // RELAY-CLIENT-04: the offline-shell bootstrap was promoted to
  // src/lib/net/offlineBootstrap.js. The OS still owns the seam: the
  // endpoints module needs its OS lsKeyPrefix re-applied per fresh module
  // graph so localStorage state lands under the OS-private key.
  const endpoints = await import('../lib/net/endpoints.js')
  endpoints.configure({ lsKeyPrefix: 'vulos.os.endpoints.v1', healthPath: '/api/auth/status' })
  return import('../lib/net/offlineBootstrap.js')
}

// navigator.serviceWorker / ServiceWorkerRegistration / ServiceWorker are big
// DOM interfaces (ServiceWorkerContainer, plus EventTarget, plus a real
// PushManager on the registration, …) that jsdom does not implement and this
// suite has no honest way to construct — offlineBootstrap.ts only ever reads
// the handful of members these fakes provide (register/addEventListener/
// installing/waiting/state/postMessage). These stay plain duck-typed
// fixtures; they're wired onto the global via setServiceWorker()'s
// Object.defineProperty below (untyped `value`, same escape hatch as the
// project's other jsdom-global stubs) rather than a direct assignment or cast.

function makeRegistration() {
  const listeners: Record<string, (() => void) | undefined> = {}
  let installing: ReturnType<typeof makeInstallingWorker> | null = null
  const reg = {
    waiting: null as ReturnType<typeof makeInstallingWorker> | null,
    installing: null as ReturnType<typeof makeInstallingWorker> | null,
    addEventListener: vi.fn((evt: string, fn: () => void) => {
      listeners[evt] = fn
    }),
    _fireUpdateFound(newWorker: ReturnType<typeof makeInstallingWorker>) {
      installing = newWorker
      reg.installing = newWorker
      listeners.updatefound?.()
    },
    _installing() { return installing },
  }
  return reg
}

function makeInstallingWorker() {
  const listeners: Record<string, Array<() => void> | undefined> = {}
  return {
    state: 'installing',
    postMessage: vi.fn(),
    addEventListener: vi.fn((evt: string, fn: () => void) => {
      if (!listeners[evt]) listeners[evt] = []
      listeners[evt]?.push(fn)
    }),
    _setState(state: string) {
      this.state = state
      ;(listeners.statechange || []).forEach((fn) => fn())
    },
  }
}

function setServiceWorker(value: unknown): void {
  Object.defineProperty(globalThis.navigator, 'serviceWorker', { value, configurable: true })
}

beforeEach(() => {
  try { localStorage.clear() } catch { /* ignore */ }
  globalThis.window = globalThis.window || {}
  globalThis.document = globalThis.document || {}
  // jsdom's document is read-only on readyState, so just shadow it for clarity.
  Object.defineProperty(globalThis.document, 'readyState', {
    value: 'complete', configurable: true, writable: true,
  })
  globalThis.navigator = globalThis.navigator || {}
})

afterEach(() => {
  vi.restoreAllMocks()
  Reflect.deleteProperty(globalThis.navigator, 'serviceWorker')
})

describe('bootstrapOffline', () => {
  it('registers the service worker exactly once across repeated calls', async () => {
    const reg = makeRegistration()
    const register = vi.fn(async () => reg)
    setServiceWorker({
      register,
      controller: {},
      addEventListener: vi.fn(),
    })
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))

    const m = await freshModule()
    m._resetForTests()
    m.bootstrapOffline()
    m.bootstrapOffline()
    m.bootstrapOffline()
    // Yield for the async register() chain.
    await Promise.resolve()
    await Promise.resolve()
    expect(register).toHaveBeenCalledTimes(1)
    expect(register).toHaveBeenCalledWith('/sw.js')
  })

  it('does not throw if serviceWorker is unavailable', async () => {
    Reflect.deleteProperty(globalThis.navigator, 'serviceWorker')
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))
    const m = await freshModule()
    m._resetForTests()
    expect(() => m.bootstrapOffline()).not.toThrow()
  })

  it('onUpdateAvailable fires once a new SW installs while controlled', async () => {
    const reg = makeRegistration()
    const register = vi.fn(async () => reg)
    setServiceWorker({
      register,
      controller: {},                 // page IS controlled by an old SW
      addEventListener: vi.fn(),
    })
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))

    const m = await freshModule()
    m._resetForTests()
    const cb = vi.fn()
    m.onUpdateAvailable(cb)
    m.bootstrapOffline()
    await Promise.resolve()
    await Promise.resolve()

    // Simulate a new SW becoming available.
    const newWorker = makeInstallingWorker()
    reg._fireUpdateFound(newWorker)
    newWorker._setState('installed')

    expect(cb).toHaveBeenCalledTimes(1)

    // applyUpdate posts SKIP_WAITING to the waiting worker.
    const applied = m.applyUpdate()
    expect(applied).toBe(true)
    expect(newWorker.postMessage).toHaveBeenCalledWith({ type: 'SKIP_WAITING' })
  })

  it('applyUpdate is a no-op when no SW is waiting', async () => {
    const m = await freshModule()
    m._resetForTests()
    expect(m.applyUpdate()).toBe(false)
  })
})
