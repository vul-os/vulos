/**
 * offlineQueue.js — local write-queue for offline operation (OFFLINE-03).
 *
 * Shape mirrors webmail-vulos/src/lib/outboxQueue.js so the OS shell, mail
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
 *       body:    string | null,    // already-serialized payload (e.g. JSON.stringify)
 *     },
 *     queuedAt:  number,           // ms epoch
 *     attempts:  number,
 *     lastError: string | null,
 *   }
 *
 * Storage: localStorage (single key). v1 cap is small (per-OS-shell write
 * actions are infrequent — file ops + settings mutations); the IndexedDB
 * upgrade path is open for later.
 */

import { request, rawFetch } from './api.js'

const LS_KEY = 'vulos.os.offlineQueue.v1'
const MAX_ATTEMPTS = 5
const FLUSH_INTERVAL_MS = 30_000

let _flushing = false
let _flushTimer = null
const listeners = new Set()

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

/** Read the full queue from localStorage. Empty array on missing/corrupt. */
export function readAll() {
  try {
    if (typeof localStorage === 'undefined') return []
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return []
    const v = JSON.parse(raw)
    return Array.isArray(v) ? v : []
  } catch {
    return []
  }
}

function writeAll(items) {
  try {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(LS_KEY, JSON.stringify(items))
    }
  } catch { /* storage may be full or unavailable */ }
}

/**
 * enqueue — append a write to the queue.
 *
 * @param {object} entry
 * @param {string} [entry.kind]    free-form caller tag
 * @param {object} entry.body      { method, path, headers, body }
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
  const items = readAll().filter((it) => it.id !== id)
  writeAll(items)
  emit()
}

/** Clear all queued records. */
export function clear() {
  writeAll([])
  emit()
}

/** Number of items currently queued. */
export function size() {
  return readAll().length
}

/**
 * Default replay strategy — routes each record through the shared API client
 * (which handles cloud↔LAN failover via lib/endpoints.js).
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
        // Success: drop from queue.
        items = readAll().filter((it) => it.id !== record.id)
        writeAll(items)
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

/** Stop the background flusher (used by tests). */
export function stopOfflineQueueFlushLoop() {
  if (_flushTimer != null) {
    clearInterval(_flushTimer)
    _flushTimer = null
  }
}
