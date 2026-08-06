/**
 * offlineQueueIDB.test.js — OFFLINE-04: IndexedDB durable backing for the OS
 * offline write-queue.
 *
 * jsdom provides no IndexedDB, so this test installs a small but faithful
 * in-memory IndexedDB fake (implementing exactly the API surface offlineQueue.js
 * uses: open/onupgradeneeded/onsuccess, transaction → objectStore →
 * getAll/put/delete/clear, tx.oncomplete). It proves:
 *
 *   • large records (well over the localStorage mirror cap) persist to IndexedDB
 *     and are restored after a "reload" (fresh module import) via ready().
 *   • BINARY bodies (Blob / ArrayBuffer) survive — they could NOT in the old
 *     localStorage-only queue (JSON.stringify drops them).
 *   • the existing synchronous queue API is unchanged.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// ── Minimal in-memory IndexedDB fake ─────────────────────────────────────────
// Keyed object store; async callbacks fire on microtasks to mimic real IDB.
//
// This fake only implements the handful of IndexedDB members offlineQueue.ts
// actually touches (open/onupgradeneeded/onsuccess, transaction →
// objectStore → getAll/put/delete/clear, tx.oncomplete) — not the full
// IDBFactory/IDBDatabase/IDBObjectStore surface (deleteDatabase, cursors,
// indexes, …). It is wired onto globalThis via Object.defineProperty (value
// typed `unknown`) rather than a direct assignment or cast, matching the
// project's other jsdom-global stubs (see offlineBootstrap.test.ts).

type Row = Record<string, unknown> & { id: unknown }

interface FakeRequest {
  onsuccess: (() => void) | null
  onerror: (() => void) | null
  result: unknown
}

function makeFakeIndexedDB() {
  const databases = new Map<string, Map<string, Map<unknown, Row>>>()

  // Schedule a callback on a macrotask so all synchronously-issued requests in a
  // transaction run their onsuccess BEFORE the transaction's oncomplete.
  function later(fn: () => void) { setTimeout(fn, 0) }

  function makeObjectStore(map: Map<unknown, Row>, pending: Array<() => void>) {
    // pending collects per-request onsuccess thunks so the transaction can run
    // them in order, then fire oncomplete.
    const issue = (apply: (req: FakeRequest) => void): FakeRequest => {
      const req: FakeRequest = { onsuccess: null, onerror: null, result: undefined }
      pending.push(() => { apply(req); if (req.onsuccess) req.onsuccess() })
      return req
    }
    return {
      keyPath: 'id',
      getAll: () => issue((req) => { req.result = Array.from(map.values()) }),
      put: (value: Row) => issue(() => { map.set(value.id, value) }), // by reference → binary intact
      delete: (key: unknown) => issue(() => { map.delete(key) }),
      clear: () => issue(() => { map.clear() }),
    }
  }

  function makeDB(name: string) {
    if (!databases.has(name)) databases.set(name, new Map())
    const stores = databases.get(name) as Map<string, Map<unknown, Row>>
    return {
      objectStoreNames: { contains: (s: string) => stores.has(s) },
      createObjectStore(s: string) {
        if (!stores.has(s)) stores.set(s, new Map())
        return makeObjectStore(stores.get(s) as Map<unknown, Row>, [])
      },
      transaction(storeName: string) {
        if (!stores.has(storeName)) stores.set(storeName, new Map())
        const map = stores.get(storeName) as Map<unknown, Row>
        const pending: Array<() => void> = []
        const store = makeObjectStore(map, pending)
        const tx: { oncomplete: (() => void) | null; onerror: (() => void) | null; onabort: (() => void) | null; error: unknown } =
          { oncomplete: null, onerror: null, onabort: null, error: null }
        // After the caller has synchronously issued its request(s) and set
        // tx.oncomplete, run them in order then complete the transaction.
        later(() => {
          for (const run of pending) run()
          if (tx.oncomplete) tx.oncomplete()
        })
        return {
          objectStore: () => store,
          get oncomplete() { return tx.oncomplete },
          set oncomplete(fn) { tx.oncomplete = fn },
          get onerror() { return tx.onerror },
          set onerror(fn) { tx.onerror = fn },
          get onabort() { return tx.onabort },
          set onabort(fn) { tx.onabort = fn },
          get error() { return tx.error },
        }
      },
      close() {},
    }
  }

  return {
    open(name: string) {
      const req: FakeRequest & { onupgradeneeded: (() => void) | null; error: unknown } =
        { onsuccess: null, onerror: null, onupgradeneeded: null, result: undefined, error: null }
      const fresh = !databases.has(name)
      req.result = makeDB(name)
      later(() => {
        if (fresh && req.onupgradeneeded) req.onupgradeneeded()
        if (req.onsuccess) req.onsuccess()
      })
      return req
    },
    _databases: databases,
  }
}

async function freshModule() {
  vi.resetModules()
  return import('../lib/offlineQueue.js')
}

// settle drains several macrotask ticks so the queue's chained IndexedDB
// transactions (clear → N puts) all commit before assertions run.
async function settle(ticks = 10) {
  for (let i = 0; i < ticks; i++) {
    await new Promise((r) => setTimeout(r, 0))
  }
}

// indexedDB is declared `IDBFactory` (a much larger surface than this fake
// implements — see the fake's own doc comment above) via a global `declare
// var`, so a direct assignment fails structurally (TS2739: missing cmp,
// databases, deleteDatabase). Installed the same way as the project's other
// duck-typed DOM-global stubs: Object.defineProperty with an untyped `value`.
function setIndexedDB(value: unknown): void {
  Object.defineProperty(globalThis, 'indexedDB', { value, configurable: true })
}

beforeEach(() => {
  try { localStorage.clear() } catch { /* ignore */ }
  globalThis.window = globalThis.window || {}
  if (!window.addEventListener) window.addEventListener = () => {}
  setIndexedDB(makeFakeIndexedDB())
})

afterEach(() => {
  vi.restoreAllMocks()
  Reflect.deleteProperty(globalThis, 'indexedDB')
})

describe('offlineQueue — IndexedDB durability (OFFLINE-04)', () => {
  it('persists a LARGE record to IndexedDB and restores it after reload', async () => {
    const q1 = await freshModule()
    await q1.ready()

    // A ~2 MB body — well over the localStorage mirror cap (1 MB).
    const big = 'x'.repeat(2_000_000)
    q1.enqueue({ kind: 'file.upload', body: { method: 'PUT', path: '/api/files/big', body: big } })
    expect(q1.size()).toBe(1)

    // Let the async IndexedDB write settle.
    await settle()

    // The localStorage mirror must have DEFERRED the large record (not stored it).
    const lsRaw = localStorage.getItem('vulos.os.offlineQueue.v1')
    expect(lsRaw).toBeTruthy()
    if (!lsRaw) throw new Error('expected a localStorage mirror entry')
    expect(lsRaw.length).toBeLessThan(100_000) // mirror did not inline 2 MB

    // "Reload": a fresh module instance must restore the large record from IDB.
    const q2 = await freshModule()
    await q2.ready()
    const items = q2.readAll()
    expect(items).toHaveLength(1)
    const restoredBig = items[0].body.body
    if (typeof restoredBig !== 'string') throw new Error('expected a string body')
    expect(restoredBig.length).toBe(2_000_000)
    expect(items[0].body.path).toBe('/api/files/big')
  })

  it('persists a BINARY (ArrayBuffer) body that survives reload', async () => {
    const q1 = await freshModule()
    await q1.ready()

    const bytes = new Uint8Array([0, 1, 2, 253, 254, 255]).buffer
    q1.enqueue({ kind: 'blob.write', body: { method: 'POST', path: '/api/blob', body: bytes } })
    await settle()

    const q2 = await freshModule()
    await q2.ready()
    const items = q2.readAll()
    expect(items).toHaveLength(1)
    const restoredBytes = items[0].body.body
    if (!(restoredBytes instanceof ArrayBuffer)) throw new Error('expected an ArrayBuffer body')
    const restored = new Uint8Array(restoredBytes)
    expect(Array.from(restored)).toEqual([0, 1, 2, 253, 254, 255])
  })

  it('delete + clear propagate to IndexedDB', async () => {
    const q1 = await freshModule()
    await q1.ready()
    const a = q1.enqueue({ body: { path: '/api/a', body: 'a' } })
    q1.enqueue({ body: { path: '/api/b', body: 'b' } })
    await settle()

    q1.remove(a.id)
    await settle()

    const q2 = await freshModule()
    await q2.ready()
    expect(q2.size()).toBe(1)
    expect(q2.readAll()[0].body.path).toBe('/api/b')

    q2.clear()
    await settle()
    const q3 = await freshModule()
    await q3.ready()
    expect(q3.size()).toBe(0)
  })
})
