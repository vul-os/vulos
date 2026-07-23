/**
 * offlineQueue.js — local write-queue for offline operation (OFFLINE-03 +
 * OFFLINE-04 IndexedDB upgrade).
 *
 * Shape mirrors lilmail/src/lib/outboxQueue.js so the OS shell, mail
 * client, and office suite behave identically when the network drops.
 *
 * When a write (POST/PUT/PATCH/DELETE) fails because the box is unreachable,
 * the caller can enqueue it here. The queue is flushed automatically:
 *   • whenever the browser fires `online`
 *   • on a 30s background ticker
 *   • on demand via flush()
 *
 * Queue record shape:
 *   {
 *     id:        string,           // local UUID (queue key)
 *     kind:      string,           // free-form caller-defined tag (e.g. 'file.upload')
 *     body: {                      // request envelope replayed verbatim
 *       method:  string,           // 'POST' | 'PUT' | 'PATCH' | 'DELETE' | …
 *       path:    string,           // absolute URL OR /api-relative path
 *       headers: object | null,
 *       body:    string | Blob | ArrayBuffer | null,  // payload (may be binary)
 *     },
 *     queuedAt:  number,           // ms epoch
 *     attempts:  number,
 *     lastError: string | null,
 *   }
 *
 * Storage (OFFLINE-04): IndexedDB is the DURABLE backing store, so large and
 * BINARY queued writes (Blob / ArrayBuffer bodies) survive offline without the
 * ~5 MB synchronous localStorage cap. To keep the EXISTING synchronous queue API
 * (readAll/size/enqueue/remove/clear are all sync), an in-memory MIRROR is the
 * source of truth for reads; every mutation writes through to IndexedDB (async,
 * durable) and best-effort to localStorage (so the mirror can be re-hydrated
 * synchronously on the next module load even before IndexedDB resolves).
 *
 * When IndexedDB is unavailable (SSR, locked-down browsers, the jsdom test env),
 * the queue degrades to the previous localStorage-only behaviour with no API
 * change.
 */

import { request, rawFetch } from './api.js'

const LS_KEY = 'vulos.os.offlineQueue.v1'
const IDB_NAME = 'vulos.os.offlineQueue'
const IDB_STORE = 'writes'
const IDB_VERSION = 1
const MAX_ATTEMPTS = 5
const FLUSH_INTERVAL_MS = 30_000
// Only mirror to localStorage when the JSON-serialisable form is comfortably
// under the ~5 MB localStorage budget; large/binary records live only in
// IndexedDB. This keeps the synchronous-hydration fast path working for the
// common (small) case without ever overflowing localStorage.
const LS_MIRROR_CAP_BYTES = 1_000_000

let _flushing = false
let _flushTimer = null
const listeners = new Set()

// In-memory mirror — the synchronous source of truth for readAll/size.
let _mirror = hydrateFromLocalStorage()
// Resolves once the initial IndexedDB hydration (if any) has completed.
let _ready = hydrateFromIndexedDB()

/** Subscribe to queue-state changes. Returns an unsubscribe fn. */
export function onQueueChange(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function emit() {
  const items = readAll()
  for (const fn of listeners) {
    try { fn(items) } catch { /* listener errors are non-fatal */ }
  }
}

function genId() {
  try {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID()
  } catch { /* fallthrough */ }
  return `q-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

// ── IndexedDB plumbing ───────────────────────────────────────────────────────

/** True when IndexedDB is usable in this environment. */
function idbAvailable() {
  try {
    return typeof indexedDB !== 'undefined' && indexedDB !== null
  } catch {
    return false
  }
}

/** Open (creating/upgrading) the queue object store. Resolves to an IDBDatabase. */
function openIDB() {
  return new Promise((resolve, reject) => {
    let req
    try {
      req = indexedDB.open(IDB_NAME, IDB_VERSION)
    } catch (err) {
      reject(err)
      return
    }
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(IDB_STORE)) {
        // keyPath 'id' so put/delete are keyed by the record id.
        db.createObjectStore(IDB_STORE, { keyPath: 'id' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error || new Error('indexedDB open failed'))
  })
}

/**
 * Run fn(store) inside a transaction of the given mode and resolve to fn's
 * result. We resolve from BOTH fn's own promise (which settles on the request's
 * onsuccess, carrying the result) AND tx.oncomplete (durability), so the value
 * is never lost to microtask-vs-macrotask ordering between onsuccess and
 * oncomplete. Errors reject.
 */
async function withStore(mode, fn) {
  const db = await openIDB()
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction(IDB_STORE, mode)
      const store = tx.objectStore(IDB_STORE)
      let result
      let fnDone = false
      let txDone = false
      const maybeResolve = () => { if (fnDone && txDone) resolve(result) }
      Promise.resolve(fn(store)).then((r) => {
        result = r
        fnDone = true
        maybeResolve()
      }).catch(reject)
      tx.oncomplete = () => { txDone = true; maybeResolve() }
      tx.onerror = () => reject(tx.error || new Error('indexedDB tx failed'))
      tx.onabort = () => reject(tx.error || new Error('indexedDB tx aborted'))
    })
  } finally {
    try { db.close() } catch { /* ignore */ }
  }
}

/** Read every record from IndexedDB (ordered by queuedAt for stable replay). */
async function idbReadAll() {
  return withStore('readonly', (store) => new Promise((resolve, reject) => {
    const req = store.getAll()
    req.onsuccess = () => {
      const rows = Array.isArray(req.result) ? req.result.slice() : []
      rows.sort((a, b) => (a.queuedAt || 0) - (b.queuedAt || 0))
      resolve(rows)
    }
    req.onerror = () => reject(req.error)
  }))
}

function idbPut(record) {
  return withStore('readwrite', (store) => { store.put(record) })
}

function idbDelete(id) {
  return withStore('readwrite', (store) => { store.delete(id) })
}

function idbClear() {
  return withStore('readwrite', (store) => { store.clear() })
}

/**
 * Persist the full record set to IndexedDB (durable), then best-effort mirror a
 * small subset to localStorage for fast synchronous re-hydration. Binary/large
 * records are kept in IndexedDB only. Fire-and-forget: failures never block the
 * synchronous caller (the in-memory mirror already reflects the change).
 */
function persist(items) {
  writeLocalStorageMirror(items)
  if (!idbAvailable()) return Promise.resolve()
  // Reconcile IndexedDB to exactly `items`: clear then re-put. The set is small
  // (queued offline writes), so a full rewrite is simplest and race-free.
  return idbClear()
    .then(() => Promise.all(items.map((it) => idbPut(it))))
    .catch(() => { /* durable store unavailable — mirror still holds the data */ })
}

// ── localStorage mirror (synchronous hydration source + fallback) ─────────────

/**
 * writeLocalStorageMirror serialises items to localStorage when they are small
 * enough and JSON-safe. Records carrying binary (Blob/ArrayBuffer) bodies or an
 * over-cap total are NOT mirrored (they live durably in IndexedDB); a marker is
 * left so a fresh load knows to wait for the IndexedDB hydration.
 */
function writeLocalStorageMirror(items) {
  if (typeof localStorage === 'undefined') return
  try {
    const jsonSafe = items.every((it) => isJsonSafeBody(it.body && it.body.body))
    if (!jsonSafe) {
      // Cannot represent binary in localStorage — defer to IndexedDB entirely.
      localStorage.setItem(LS_KEY, JSON.stringify({ deferred: true }))
      return
    }
    const serialized = JSON.stringify(items)
    if (serialized.length > LS_MIRROR_CAP_BYTES) {
      localStorage.setItem(LS_KEY, JSON.stringify({ deferred: true }))
      return
    }
    localStorage.setItem(LS_KEY, serialized)
  } catch { /* storage may be full or unavailable — IndexedDB remains durable */ }
}

/** A body is JSON-safe if it is null, a string, or absent (not Blob/ArrayBuffer). */
function isJsonSafeBody(body) {
  if (body == null) return true
  if (typeof body === 'string') return true
  return false
}

/** Synchronously hydrate the mirror from localStorage. [] on missing/corrupt/deferred. */
function hydrateFromLocalStorage() {
  try {
    if (typeof localStorage === 'undefined') return []
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return []
    const v = JSON.parse(raw)
    if (Array.isArray(v)) return v
    // { deferred: true } or any non-array → empty until IndexedDB hydrates.
    return []
  } catch {
    return []
  }
}

/**
 * Asynchronously hydrate the mirror from IndexedDB (the durable store). When
 * IndexedDB holds records the localStorage mirror could not (binary/large), this
 * is what restores them after a reload. IndexedDB is authoritative on init.
 */
async function hydrateFromIndexedDB() {
  if (!idbAvailable()) return
  try {
    const rows = await idbReadAll()
    if (rows.length > 0) {
      _mirror = rows
      emit()
    }
  } catch { /* durable store unavailable — keep the localStorage-hydrated mirror */ }
}

/**
 * ready — resolves once the initial IndexedDB hydration has completed. Optional;
 * the synchronous API works immediately off the localStorage-hydrated mirror,
 * but a caller (or test) that needs the durable set restored can await this.
 */
export function ready() {
  return _ready
}

// ── Public queue API (synchronous, mirror-backed) ────────────────────────────

/** Read the full queue. Synchronous — returns a copy of the in-memory mirror. */
export function readAll() {
  return _mirror.slice()
}

/** Replace the mirror and write through to the durable + fallback stores. */
function writeAll(items) {
  _mirror = items.slice()
  persist(_mirror)
}

/**
 * enqueue — append a write to the queue.
 *
 * @param {object} entry
 * @param {string} [entry.kind]    free-form caller tag
 * @param {object} entry.body      { method, path, headers, body }
 *   body.body may be a string, a Blob, or an ArrayBuffer — binary survives via
 *   IndexedDB (it could not in the old localStorage-only queue).
 * @returns {object} the stored record
 */
export function enqueue(entry) {
  const body = entry && entry.body ? entry.body : {}
  const record = {
    id: genId(),
    kind: (entry && entry.kind) || 'write',
    body: {
      method: body.method || 'POST',
      path: body.path || '',
      headers: body.headers || null,
      body: body.body ?? null,
    },
    queuedAt: Date.now(),
    attempts: 0,
    lastError: null,
  }
  const items = readAll()
  items.push(record)
  writeAll(items)
  emit()
  return record
}

/** Remove a record by id. */
export function remove(id) {
  writeAll(readAll().filter((it) => it.id !== id))
  emit()
}

/** Clear all queued records. */
export function clear() {
  writeAll([])
  emit()
}

/** Number of items currently queued. */
export function size() {
  return _mirror.length
}

/**
 * Default replay strategy — routes each record through the shared API client
 * (which handles cloud↔LAN failover via @vulos/relay-client/endpoints).
 *
 * Records with a path starting with '/api' are stripped to the post-/api
 * suffix and replayed via request(); anything else is replayed via rawFetch()
 * so it still benefits from failover.
 */
async function defaultReplay(record) {
  const { method, path, headers, body } = record.body || {}
  if (!path) throw new Error('offlineQueue: empty path')
  const init = {
    method: method || 'POST',
    headers: headers || undefined,
    body: body ?? undefined,
  }
  if (path.startsWith('/api/')) {
    return request(path.slice(4), init) // request() re-prefixes with /api
  }
  const res = await rawFetch(path.replace(/^\/api/, ''), init)
  if (!res || !res.ok) {
    const status = res ? res.status : 0
    throw new Error(`offlineQueue: replay ${path} → ${status}`)
  }
  return res
}

/**
 * flush — attempt to replay every queued record. Stops on the first network
 * failure; retried later when `online` fires or the ticker re-arms.
 *
 * @param {object} [deps] — optional { replay(record): Promise } override for
 *   tests so the queue is exercised without touching api.js.
 * @returns {Promise<{ sent: number, failed: number, remaining: number }>}
 */
export async function flush(deps) {
  if (_flushing) return { sent: 0, failed: 0, remaining: size() }
  _flushing = true
  const replay = (deps && deps.replay) || defaultReplay
  let sent = 0
  let failed = 0
  try {
    let items = readAll()
    for (const record of items) {
      try {
        await replay(record)
        // Success: drop from queue (durable delete + mirror update).
        items = readAll().filter((it) => it.id !== record.id)
        writeAll(items)
        if (idbAvailable()) { try { await idbDelete(record.id) } catch { /* mirror authoritative */ } }
        sent++
      } catch (err) {
        record.attempts++
        record.lastError = String(err?.message ?? err)
        if (record.attempts >= MAX_ATTEMPTS) {
          // Poisoned record — give up so the queue doesn't get stuck forever.
          items = readAll().filter((it) => it.id !== record.id)
        } else {
          items = readAll().map((it) => (it.id === record.id ? record : it))
        }
        writeAll(items)
        failed++
        // First failure → stop; we'll retry the rest on the next online/tick.
        break
      }
    }
  } finally {
    _flushing = false
    emit()
  }
  return { sent, failed, remaining: size() }
}

/**
 * startOfflineQueueFlushLoop — install background flusher.
 *
 * Listens for `online` and re-arms a 30s ticker. Safe to call multiple times;
 * subsequent calls are no-ops.
 *
 * @param {object} [deps] — optional { replay } override for tests.
 */
export function startOfflineQueueFlushLoop(deps) {
  if (typeof window === 'undefined') return

  const tryFlush = async () => {
    if (size() === 0) return
    try { await flush(deps) } catch { /* swallow — re-try later */ }
  }

  if (window.addEventListener) {
    window.addEventListener('online', tryFlush)
  }
  if (_flushTimer == null) {
    _flushTimer = setInterval(tryFlush, FLUSH_INTERVAL_MS)
  }
  // First-shot attempt right after registration.
  tryFlush()
}
