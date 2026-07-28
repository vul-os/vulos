// @vulos/offline — generic, host-agnostic offline persistence for local-first apps.
//
// DESIGN (generalized from diwan + flowstock, roadmap/OFFLINE-DATA.md):
//   - Works STANDALONE on any host — no Vulos, no shell, no network. An app that
//     vendors this module gets offline-first local persistence for a Yjs doc.
//   - Lights up EXTRA safety under Vulos: if the OS offline gate is present
//     (window.vulos.offline, OFFLINE-AUTH-01), the local cache is encrypted at
//     rest with a per-app key and can be gated on unlock. Without Vulos it stores
//     plaintext and relies on the device's own encryption — the SAME honest
//     posture diwan/flowstock already ship, just made explicit and swappable.
//   - Fail-safe, not fail-open: encryption is used WHEN a key is available; its
//     absence never blocks the app from working (offline access must not depend
//     on a host that may not be there). The app can query offlineStatus() and
//     tell the user which protection is active — never overclaim.
//
// Dependencies: `yjs` (peer) + WebCrypto only. No Vulos imports — the host is
// detected via a global, so this file is copy/vendor-able into any OSS app.

import * as Y from 'yjs'

// ─── Host detection (Vulos-optional) ──────────────────────────────────────────

// offlineHost returns the OS offline gate if a compatible host injected one
// (window.vulos.offline from OFFLINE-AUTH-01), else null. Never throws.
export function offlineHost() {
  try {
    const h = typeof window !== 'undefined' && window.vulos && window.vulos.offline
    if (h && typeof h.appKey === 'function' && typeof h.isUnlocked === 'function') return h
  } catch { /* no window / sealed */ }
  return null
}

// resolveOfflineKey returns the AES-GCM CryptoKey to encrypt this app's cache
// with, or null to store plaintext (relying on device encryption). It asks the
// host for the app's per-app key; a locked or absent host yields null. An
// explicit `override` key (e.g. an app that manages its own passphrase) wins.
export async function resolveOfflineKey(override) {
  if (override) return override
  const h = offlineHost()
  if (!h) return null
  try { return (await h.appKey()) || null } catch { return null }
}

// offlineStatus reports what protection is actually active, so the app can be
// honest with the user. `host`: a Vulos gate is present. `unlocked`: it is
// unlocked. `encryptedAtRest`: whether a key is available to encrypt the cache.
export async function offlineStatus(override) {
  const h = offlineHost()
  const key = await resolveOfflineKey(override)
  let unlocked = false
  if (h) { try { unlocked = await h.isUnlocked() } catch { unlocked = false } }
  return { host: !!h, unlocked, encryptedAtRest: !!key }
}

// ─── Storage backend (injectable for tests; real = IndexedDB) ─────────────────

const DB_PREFIX = 'vulos-offline-'
const STORE = 'kv'
const SNAP_KEY = 'snapshot'

function idbBackend(dbName) {
  function open() {
    return new Promise((resolve, reject) => {
      const req = indexedDB.open(dbName, 1)
      req.onupgradeneeded = () => req.result.createObjectStore(STORE)
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => reject(req.error)
      req.onblocked = () => reject(new Error('offline: IndexedDB open blocked'))
    })
  }
  function tx(mode, fn) {
    return open().then(db => new Promise((resolve, reject) => {
      const t = db.transaction(STORE, mode)
      const r = fn(t.objectStore(STORE))
      // Close the connection after each transaction — opening per-op and never
      // closing leaks connections and can spuriously block a future version bump.
      t.oncomplete = () => { const v = r && r.result !== undefined ? r.result : null; try { db.close() } catch { /* */ } resolve(v) }
      t.onerror = () => { try { db.close() } catch { /* */ } reject(t.error) }
      t.onabort = () => { try { db.close() } catch { /* */ } reject(t.error) }
    }))
  }
  return {
    get: (k) => tx('readonly', s => s.get(k)),
    set: (k, v) => tx('readwrite', s => s.put(v, k)),
    del: (k) => tx('readwrite', s => s.delete(k)),
  }
}

// ─── AES-GCM codec (only used when a key is present) ──────────────────────────

function subtle() {
  const s = globalThis.crypto && globalThis.crypto.subtle
  if (!s) throw new Error('offline: WebCrypto unavailable')
  return s
}

// seal wraps a Yjs update as { iv, ct } under `key`; a fresh random 12-byte IV
// per write (AES-GCM key/IV pairs must never repeat under a long-lived key).
async function seal(key, bytes) {
  const iv = globalThis.crypto.getRandomValues(new Uint8Array(12))
  const ct = new Uint8Array(await subtle().encrypt({ name: 'AES-GCM', iv }, key, bytes))
  return { iv, ct }
}

async function open(key, rec) {
  const pt = await subtle().decrypt({ name: 'AES-GCM', iv: rec.iv }, key, rec.ct)
  return new Uint8Array(pt)
}

// ─── Persistence provider ─────────────────────────────────────────────────────

// persistYDoc gives a Yjs doc durable, offline-first local persistence.
//
//   const p = await persistYDoc(ydoc, { name: 'notes:'+id, key })
//   await p.whenSynced   // local state loaded and applied to ydoc
//   ...                  // edits are debounced-persisted automatically
//   p.destroy()          // stop persisting (call on teardown)
//
// Model: a single debounced snapshot (Y.encodeStateAsUpdate) per doc. Simple and
// correct; an incremental append-log (diwan-style) is a future optimization. If
// `key` is provided the snapshot is AES-GCM sealed at rest; otherwise it is
// stored plaintext (device-encryption reliant — offlineStatus() reports which).
//
// This does NOT sync to a server/peer — that is the app's existing transport
// (Notes' collab WebSocket, etc.); this is the LOCAL half. A Yjs doc converges
// with the server on reconnect regardless, so local + remote compose cleanly.
export async function persistYDoc(ydoc, opts = {}) {
  const { name, key = null, debounceMs = 400, store } = opts
  if (!ydoc) throw new Error('offline: persistYDoc requires a Y.Doc')
  if (!name || typeof name !== 'string') throw new Error('offline: persistYDoc requires a name')
  const backend = store || idbBackend(opts.dbName || (DB_PREFIX + name))

  let destroyed = false
  let timer = null
  // `sealed` = the persisted cache is/should be encrypted (from a loaded enc:1
  // record, or once we write encrypted). It is the guard against a silent
  // encrypted→plaintext DOWNGRADE: if the cache is sealed but this session has no
  // key (host locked/absent), we refuse to WRITE — otherwise an edit would
  // overwrite the ciphertext with plaintext and destroy the old encrypted data.
  let sealed = false

  // doSave writes the current doc snapshot. NOT guarded by `destroyed` so an
  // explicit flush()/destroy() can still persist a pending edit; new debounced
  // saves are stopped by onUpdate's guard + clearing the timer.
  const doSave = async () => {
    try {
      const update = Y.encodeStateAsUpdate(ydoc)
      if (sealed && !key) return // fail-closed for writes over a sealed cache
      const rec = key ? { enc: 1, ...(await seal(key, update)) } : { enc: 0, u: update }
      if (key) sealed = true
      await backend.set(SNAP_KEY, rec)
    } catch { /* best-effort; a failed local save must not break editing */ }
  }

  const onUpdate = () => {
    if (destroyed) return
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => { timer = null; doSave() }, debounceMs)
  }

  // Best-effort flush when the page is being hidden/unloaded, to shrink the
  // debounce loss window. Async writes may not complete on a hard unload — this
  // reduces the risk, it does not eliminate it.
  let onHide = null
  if (typeof window !== 'undefined' && window.addEventListener) {
    onHide = () => { if (!destroyed) doSave() }
    window.addEventListener('pagehide', onHide)
    window.addEventListener('visibilitychange', onHide)
  }

  // Load persisted state and apply it to the doc, then start persisting changes.
  const whenSynced = (async () => {
    let rec = null
    try { rec = await backend.get(SNAP_KEY) } catch { rec = null }
    if (rec && !destroyed) {
      sealed = !!rec.enc // remember the on-disk protection level (downgrade guard)
      try {
        let update
        if (rec.enc) {
          if (!key) throw new Error('offline: cache is encrypted but no key available')
          update = await open(key, rec)
        } else {
          update = rec.u instanceof Uint8Array ? rec.u : new Uint8Array(rec.u || [])
        }
        Y.applyUpdate(ydoc, update, 'offline-load')
      } catch {
        // Undecryptable/corrupt snapshot: start clean rather than throw. (An app
        // that requires the encrypted cache should gate on offlineStatus() first.)
      }
    }
    if (!destroyed) ydoc.on('update', onUpdate)
    return true
  })()

  const removeHideHandlers = () => {
    if (!onHide) return
    try {
      window.removeEventListener('pagehide', onHide)
      window.removeEventListener('visibilitychange', onHide)
    } catch { /* */ }
    onHide = null
  }

  return {
    whenSynced,
    encrypted: !!key,
    // Flush any pending write immediately (e.g. before unload). Works even after
    // destroy() — it is an explicit request, not a debounced one.
    flush: doSave,
    // Stop auto-persisting and FLUSH any pending edit. Returns the flush promise
    // so callers can await durability before teardown.
    destroy() {
      if (destroyed) return undefined
      destroyed = true
      if (timer) { clearTimeout(timer); timer = null }
      try { ydoc.off('update', onUpdate) } catch { /* already gone */ }
      removeHideHandlers()
      return doSave() // final flush of any pending edit
    },
    // Erase this doc's local cache (e.g. on sign-out / forget-offline-data).
    async clear() { sealed = false; try { await backend.del(SNAP_KEY) } catch { /* ignore */ } },
  }
}
