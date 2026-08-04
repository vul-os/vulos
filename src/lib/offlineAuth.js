// offlineAuth.js — the OS offline auth gate (OFFLINE-AUTH-01).
//
// See roadmap/OFFLINE-AUTH.md. The split: each APP decides what it caches; the
// OS owns AUTH. This module is the OS side — nothing app-specific.
//
// Secure by construction, not by UI check. We reuse the SHIPPED client-side
// master-key crypto (lib/masterKey.js): at online login the browser already
// unwraps a password-wrapped master-key envelope entirely client-side (PBKDF2 ->
// AES-256-GCM, fail-closed via the GCM tag). Offline auth caches that opaque
// envelope and unwraps it locally with the account password. A WRONG PASSWORD
// FAILS THE GCM TAG AND YIELDS NOTHING — there is no "valid=true" bypass path.
//
// Honest posture (roadmap/OFFLINE-AUTH.md + mobile/DECISIONS.md MOB-04): in v1
// the unlocked key gates the running SESSION and derives per-app keys; the cache
// itself is still plaintext IndexedDB, so AT REST the protection is the device's
// own encryption (Android FBE / OS disk). Never describe this as end-to-end.

import {
  unwrapMasterKeyWithPassword,
  deriveContentKey,
  holdMasterKey,
  getMasterKey,
  clearMasterKey,
} from './masterKey.js'
import { requireSubtle } from './secureContext.js'

const DB_NAME = 'vulos-offline-auth'
const STORE_NAME = 'kv'

const K_ENVELOPE = 'envelope' // the password-wrapped master-key slot {v,kdf,iter,salt,iv,ct}
const K_IDENTITY = 'identity' // { id, name } — who to prompt on the offline lock screen
const K_ATTEMPTS = 'attempts' // consecutive wrong offline unlocks

// Client-side PBKDF2 iteration FLOOR. The legitimate envelope uses 600k (see
// masterKey.js). We refuse to cache or unwrap anything weaker than this floor so
// that a MITM/box-compromise cannot slip a downgraded (e.g. iter=1) envelope that
// would then sit on disk and unwrap cheaply offline. Below the legit 600k with
// margin, so a future server bump won't trip it.
const MIN_PBKDF2_ITERS = 100000

// validEnvelope structurally checks a password-wrapped slot BEFORE any unwrap, so
// a malformed/downgraded envelope is rejected as a structural error (never as a
// wrong-password ATTEMPT — a corrupted envelope must not count toward the wipe).
function validEnvelope(slot) {
  return !!(slot && typeof slot === 'object' &&
    slot.kdf === 'pbkdf2-sha256' &&
    (slot.iter | 0) >= MIN_PBKDF2_ITERS &&
    slot.salt && slot.iv && slot.ct)
}

// After this many consecutive wrong offline unlocks we WIPE the cached envelope
// (and signal an app-cache wipe): a stolen device degrades to "nothing cached,
// must go online", rather than being an oracle to brute-force offline. Higher
// than the server PIN's 5 because offline typos are unrecoverable (wipe, not a
// timed lockout) and the credential here is the full password, not a 4-digit PIN.
export const MAX_OFFLINE_ATTEMPTS = 10

// ─── Storage backend (injectable for tests; real = IndexedDB) ─────────────────
// jsdom has no IndexedDB, and we never want the offline-auth store to depend on a
// test polyfill. The default backend is IndexedDB; tests inject an in-memory map.

function idbStore() {
  function open() {
    return new Promise((resolve, reject) => {
      const req = indexedDB.open(DB_NAME, 1)
      req.onupgradeneeded = () => req.result.createObjectStore(STORE_NAME)
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => reject(req.error)
      // A blocked open (e.g. another tab holding an old DB version) would otherwise
      // never settle and hang every caller. Reject so callers fail fast; the boot
      // path (App.jsx) also races this against a timeout.
      req.onblocked = () => reject(new Error('offlineAuth: IndexedDB open blocked'))
    })
  }
  function tx(mode, fn) {
    return open().then(db => new Promise((resolve, reject) => {
      const t = db.transaction(STORE_NAME, mode)
      const os = t.objectStore(STORE_NAME)
      const req = fn(os)
      t.oncomplete = () => resolve(req && req.result !== undefined ? req.result : null)
      t.onerror = () => reject(t.error)
      t.onabort = () => reject(t.error)
    }))
  }
  return {
    get: (k) => tx('readonly', os => os.get(k)),
    set: (k, v) => tx('readwrite', os => os.put(v, k)),
    del: (k) => tx('readwrite', os => os.delete(k)),
  }
}

let _store = null
function store() {
  if (!_store) _store = idbStore()
  return _store
}

// __setStoreForTests injects an in-memory KV so the crypto/attempt/wipe logic can
// be tested under jsdom without an IndexedDB polyfill. Not used in production.
export function __setStoreForTests(s) {
  _store = s
}

// ─── Enrollment (online) ──────────────────────────────────────────────────────

// cacheEnvelope persists the opaque password-wrapped master-key slot so it can be
// unwrapped offline later. Called after a successful ONLINE login (the box is
// reachable then). Caching this is no weaker than the server holding it — it is
// designed to be unwrapped client-side, and 600k-iteration PBKDF2 + AES-GCM
// stands between an extracted blob and the key. Resets the attempt counter.
export async function cacheEnvelope(slot) {
  if (!validEnvelope(slot)) {
    // Refuse to persist a malformed or DOWNGRADED envelope — otherwise a weak
    // (e.g. iter=1) slot served by a compromised path would sit on disk and
    // unwrap cheaply offline later.
    throw new Error('offlineAuth: refusing to cache an invalid or weak envelope')
  }
  await store().set(K_ENVELOPE, slot)
  await store().del(K_ATTEMPTS)
}

// cacheIdentity records who the enrolled user is, so the offline lock screen can
// greet them by name. Non-secret; never includes credentials.
export async function cacheIdentity(identity) {
  if (!identity) return
  await store().set(K_IDENTITY, { id: identity.id || '', name: identity.name || '' })
}

export async function getCachedIdentity() {
  return store().get(K_IDENTITY)
}

// isEnrolled reports whether this device can attempt an offline unlock at all.
// No envelope => offline access is impossible (fail-closed): it must be earned by
// a prior successful online login.
export async function isEnrolled() {
  return !!(await store().get(K_ENVELOPE))
}

// ─── Unlock / lock (offline) ──────────────────────────────────────────────────

// unlockOffline unwraps the cached envelope with `password` and holds the master
// key in memory for the session. Returns true on success. Throws (fail-closed) on:
//   - NOT_ENROLLED: no cached envelope on this device.
//   - WRONG_PASSWORD: GCM auth failed; carries attemptsLeft.
//   - WIPED: the failure that crossed MAX_OFFLINE_ATTEMPTS; the cache was wiped.
// Serialize unlock attempts. Without this, a scripted attacker firing several
// unlockOffline() calls concurrently could have them all read the same stale
// attempt count before any write lands, undercounting toward the wipe cap. The
// UI's busy-state only serializes clicks, not programmatic calls — enforce it in
// the module. (This closes the concurrent-undercount race; it does NOT make the
// counter tamper-proof — see the honesty note in roadmap/OFFLINE-AUTH.md.)
let _unlockChain = Promise.resolve()

export function unlockOffline(password) {
  const run = () => _unlockOfflineImpl(password)
  const p = _unlockChain.then(run, run) // run after the prior attempt settles either way
  _unlockChain = p.then(() => {}, () => {}) // keep the chain alive across rejections
  return p
}

async function _unlockOfflineImpl(password) {
  const slot = await store().get(K_ENVELOPE)
  if (!slot) {
    const e = new Error('No offline access is set up on this device. Sign in online first.')
    e.code = 'NOT_ENROLLED'
    throw e
  }
  if (!validEnvelope(slot)) {
    // Corrupted/downgraded stored envelope. A STRUCTURAL failure, not a wrong
    // password — do NOT count it toward the wipe cap. The envelope is useless, so
    // wipe it (fires the wipe event → the shell drops back to online login rather
    // than trapping the user on a dead offline screen).
    await wipe()
    const e = new Error('Your offline data is corrupted. Sign in online to restore it.')
    e.code = 'CORRUPT'
    throw e
  }

  let mk
  try {
    mk = await unwrapMasterKeyWithPassword(slot, password)
  } catch {
    // The AES-GCM tag failed => wrong password. Count it; wipe at the cap.
    const attempts = ((await store().get(K_ATTEMPTS)) | 0) + 1
    await store().set(K_ATTEMPTS, attempts)
    if (attempts >= MAX_OFFLINE_ATTEMPTS) {
      await wipe()
      const e = new Error('Too many attempts — offline data on this device has been wiped. Sign in online to restore it.')
      e.code = 'WIPED'
      throw e
    }
    const left = MAX_OFFLINE_ATTEMPTS - attempts
    const e = new Error(`Incorrect password — ${left} attempt${left === 1 ? '' : 's'} left before offline data is wiped.`)
    e.code = 'WRONG_PASSWORD'
    e.attemptsLeft = left
    throw e
  }

  // Defensive: a successful decrypt that isn't a 32-byte key is a corrupt
  // envelope, not a valid unlock — fail closed without holding anything, and wipe
  // the useless envelope so the shell can fall back to online login.
  if (!(mk instanceof Uint8Array) || mk.length !== 32) {
    await wipe()
    const e = new Error('Your offline data is corrupted. Sign in online to restore it.')
    e.code = 'CORRUPT'
    throw e
  }

  holdMasterKey(mk)
  await store().del(K_ATTEMPTS)
  return true
}

// isUnlocked is the gate apps check before rendering cached data. It is true only
// while the master key is held in memory (this session, since the last unlock).
export function isUnlocked() {
  return getMasterKey() != null
}

// lock drops the in-memory key (idle lock / logout / manual lock). The next
// offline access requires the password again. The cached envelope is retained so
// the user can unlock again — use wipe() to remove it.
export function lock() {
  clearMasterKey()
}

// ─── Per-app keys ─────────────────────────────────────────────────────────────

// appKey derives a per-app AES-GCM key via HKDF from the master key, so an XSS in
// one app cannot read another app's offline cache (the mobile security floor's
// per-origin isolation, extended to offline key material). The returned CryptoKey
// is NON-EXTRACTABLE; the raw master key never leaves this module. This is the
// codec key apps will use once cache-at-rest encryption lands (MOB-04); until
// then it is a ready seam.
export async function appKey(appId) {
  const mk = getMasterKey()
  if (!mk) {
    const e = new Error('offlineAuth: locked — cannot derive an app key')
    e.code = 'LOCKED'
    throw e
  }
  const raw = await deriveContentKey(mk, 'vulos-offline-app', String(appId))
  // requireSubtle() rather than a bare `crypto.subtle` deref: on the shipped box
  // reached over plain http:// on a LAN IP or .local name this is undefined, and
  // the raw deref threw "Cannot read properties of undefined (reading
  // 'importKey')" with no hint of the cause or the fix.
  const subtle = requireSubtle()
  return subtle.importKey('raw', raw, { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt'])
}

// ─── Wipe ─────────────────────────────────────────────────────────────────────

// wipe removes the offline credential (and identity/attempts) and signals apps to
// drop their caches. Invoked on the attempt cap, and available for an explicit
// "forget offline data on this device". Best-effort on the app-cache side — apps
// own their stores, so we broadcast an event they listen for; the OS only
// guarantees the credential is gone (no further offline unlock is possible).
export async function wipe() {
  // Do NOT clearMasterKey() here. The master key is a SHARED session singleton
  // (masterKey.js) held by the ONLINE session too, and "Forget offline data" is
  // reachable only while online-authenticated — zeroing it would break in-session
  // content decryption (Drive etc.) with a misleading "unlock your account"
  // error. wipe() removes only the offline CREDENTIAL; the live session key is the
  // caller's concern (lock() on logout/idle clears it). At the attempt-cap wipe
  // path no key is held anyway (the guesses failed), so nothing is lost there.
  await store().del(K_ENVELOPE)
  await store().del(K_IDENTITY)
  await store().del(K_ATTEMPTS)
  try {
    if (typeof window !== 'undefined' && window.dispatchEvent) {
      window.dispatchEvent(new CustomEvent('vulos:offline-wipe'))
    }
  } catch { /* non-DOM context */ }
}
