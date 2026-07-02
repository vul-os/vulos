// masterKey.js — CLIENT-SIDE master-key unwrap (WAVE2-RECOVERY, Tier-1).
//
// The server stores only a doubly-wrapped MASTER KEY envelope and never holds the
// key in the clear. At login the browser fetches the password-wrapped slot and
// unwraps it HERE, entirely client-side, using the platform WebCrypto SubtleCrypto
// API with ZERO third-party JavaScript. The recovered key lives only in memory for
// the session (see holdMasterKey) and is never written to storage or logged.
//
// Wire format is produced by backend/internal/auth/masterkey.go and is byte-for-
// byte compatible:
//   - password slot: PBKDF2-HMAC-SHA256(password, salt, iter) -> AES-256-GCM key
//   - phrase slot:   BIP39 seed = PBKDF2-HMAC-SHA512(mnemonic, "mnemonic", 2048)
//                    -> HKDF-SHA256(seed, zeroSalt, info) -> AES-256-GCM key
//   - content keys:  HKDF-SHA256(masterKey, zeroSalt, "vulos-content:<domain>:<id>")
//
// Fail-closed: every unwrap authenticates via AES-GCM; a wrong password/phrase or
// any tampering throws — no partial or unauthenticated key material is returned.

const AAD_PW = 'vulos-mk-pw-v1'
const AAD_PHRASE = 'vulos-mk-phrase-v1'
const HKDF_PHRASE_INFO = 'vulos-masterkey-wrap-v1'
const ZERO_SALT_32 = new Uint8Array(32)

const te = new TextEncoder()

function subtle() {
  const s = globalThis.crypto && globalThis.crypto.subtle
  if (!s) throw new Error('masterKey: WebCrypto SubtleCrypto unavailable (requires a secure context)')
  return s
}

// b64ToBytes decodes standard base64 (as emitted by Go's encoding/json for []byte).
function b64ToBytes(b64) {
  if (b64 instanceof Uint8Array) return b64
  const bin = atob(String(b64))
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

// bytesToB64 encodes to standard base64 — the shape Go's encoding/json expects for
// a []byte field, so a slot produced here round-trips into the Go wrapSlot verbatim.
function bytesToB64(u8) {
  let bin = ''
  for (let i = 0; i < u8.length; i++) bin += String.fromCharCode(u8[i])
  return btoa(bin)
}

// PBKDF2_ITERS mirrors backend mkPBKDF2Iters — the standard password-wrap cost.
// The server enforces this as a FLOOR (mkMinPBKDF2Iters), rejecting anything lower.
const PBKDF2_ITERS = 600000

// normaliseMnemonic mirrors auth/recovery.go: lowercase + collapse whitespace.
function normaliseMnemonic(m) {
  return String(m).trim().toLowerCase().split(/\s+/).join(' ')
}

// unwrapMasterKeyWithPassword unwraps the master key from the password slot.
// `slot` is the object returned by GET /api/auth/masterkey/envelope:
//   { v, kdf, iter, salt, iv, ct }  (salt/iv/ct base64)
// Returns a Uint8Array(32). Throws on wrong password / tampering.
export async function unwrapMasterKeyWithPassword(slot, password) {
  if (!slot || slot.kdf !== 'pbkdf2-sha256') {
    throw new Error('masterKey: unsupported password kdf')
  }
  const iter = slot.iter | 0
  if (iter <= 0) throw new Error('masterKey: invalid iterations')
  const baseKey = await subtle().importKey('raw', te.encode(password), 'PBKDF2', false, ['deriveKey'])
  const aesKey = await subtle().deriveKey(
    { name: 'PBKDF2', salt: b64ToBytes(slot.salt), iterations: iter, hash: 'SHA-256' },
    baseKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['decrypt'],
  )
  const mk = await subtle().decrypt(
    { name: 'AES-GCM', iv: b64ToBytes(slot.iv), additionalData: te.encode(AAD_PW) },
    aesKey,
    b64ToBytes(slot.ct),
  )
  return new Uint8Array(mk)
}

// unwrapMasterKeyWithPhrase unwraps the master key from the recovery-phrase slot
// ({ kdf:'bip39-hkdf-sha256', iv, ct }). This is the client-side recovery path.
export async function unwrapMasterKeyWithPhrase(slot, mnemonic) {
  if (!slot || slot.kdf !== 'bip39-hkdf-sha256') {
    throw new Error('masterKey: unsupported phrase kdf')
  }
  const norm = normaliseMnemonic(mnemonic)
  // BIP39 seed: PBKDF2-HMAC-SHA512(mnemonic, "mnemonic", 2048, 64 bytes).
  const seedKey = await subtle().importKey('raw', te.encode(norm), 'PBKDF2', false, ['deriveBits'])
  const seed = new Uint8Array(await subtle().deriveBits(
    { name: 'PBKDF2', salt: te.encode('mnemonic'), iterations: 2048, hash: 'SHA-512' },
    seedKey,
    512,
  ))
  const hkdfKey = await subtle().importKey('raw', seed, 'HKDF', false, ['deriveKey'])
  const aesKey = await subtle().deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: ZERO_SALT_32, info: te.encode(HKDF_PHRASE_INFO) },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['decrypt'],
  )
  const mk = await subtle().decrypt(
    { name: 'AES-GCM', iv: b64ToBytes(slot.iv), additionalData: te.encode(AAD_PHRASE) },
    aesKey,
    b64ToBytes(slot.ct),
  )
  return new Uint8Array(mk)
}

// wrapMasterKeyWithPassword seals `masterKey` under `password`, producing a slot
// byte-for-byte compatible with backend/internal/auth/masterkey.go's password wrap
// (PBKDF2-HMAC-SHA256 -> AES-256-GCM, AAD 'vulos-mk-pw-v1'). Returns
// { v, kdf, iter, salt, iv, ct } with salt/iv/ct base64-encoded (matching Go's JSON
// []byte encoding), ready to POST. Runs entirely CLIENT-SIDE: the master key never
// leaves the tab and is never sent to the server — only this wrapped slot is.
export async function wrapMasterKeyWithPassword(masterKey, password, iter = PBKDF2_ITERS) {
  if (!(masterKey instanceof Uint8Array) || masterKey.length !== 32) {
    throw new Error('masterKey: master key must be 32 bytes')
  }
  if (!password) throw new Error('masterKey: password must not be empty')
  const salt = globalThis.crypto.getRandomValues(new Uint8Array(16))
  const iv = globalThis.crypto.getRandomValues(new Uint8Array(12))
  const baseKey = await subtle().importKey('raw', te.encode(password), 'PBKDF2', false, ['deriveKey'])
  const aesKey = await subtle().deriveKey(
    { name: 'PBKDF2', salt, iterations: iter, hash: 'SHA-256' },
    baseKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  )
  const ct = new Uint8Array(await subtle().encrypt(
    { name: 'AES-GCM', iv, additionalData: te.encode(AAD_PW) },
    aesKey,
    masterKey,
  ))
  return {
    v: 1,
    kdf: 'pbkdf2-sha256',
    iter,
    salt: bytesToB64(salt),
    iv: bytesToB64(iv),
    ct: bytesToB64(ct),
  }
}

// resetPasswordWithActiveSession performs the Tier-2 (active-session / trusted-
// device) password reset: it re-wraps the IN-MEMORY master key under `newPassword`
// CLIENT-SIDE and posts the wrapped slot + new password to the session-authed
// endpoint. The server never sees the master key — zero-access is preserved.
//
// If this session does not hold the master key (legacy login, or the key was
// cleared), it throws an error tagged `code === 'NO_MASTER_KEY'` so the caller can
// fall back to the recovery phrase (Tier-1) instead of silently failing.
export async function resetPasswordWithActiveSession(newPassword, fetchImpl = fetch) {
  const mk = getMasterKey()
  if (!mk) {
    const e = new Error('This session does not hold your encryption key — use your recovery phrase instead.')
    e.code = 'NO_MASTER_KEY'
    throw e
  }
  const slot = await wrapMasterKeyWithPassword(mk, newPassword)
  const res = await fetchImpl('/api/auth/masterkey/reset-active', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ new_password: newPassword, password_slot: slot }),
  })
  if (res.status === 409) {
    const e = new Error('This session does not hold your encryption key — use your recovery phrase instead.')
    e.code = 'NO_MASTER_KEY'
    throw e
  }
  if (!res.ok) {
    let msg = `reset failed (${res.status})`
    try {
      const j = await res.json()
      if (j && j.error) msg = j.error
    } catch { /* non-JSON error body */ }
    throw new Error(msg)
  }
  return { ok: true }
}

// deriveContentKey mirrors internal/auth.DeriveContentKey — the wave-3 entry
// point. HKDF-SHA256(masterKey, zeroSalt, "vulos-content:"+len(domain)+domain+
// len(id)+id). Returns a Uint8Array(32).
export async function deriveContentKey(masterKey, domain, id) {
  if (!(masterKey instanceof Uint8Array) || masterKey.length !== 32) {
    throw new Error('masterKey: master key must be 32 bytes')
  }
  const dom = te.encode(domain)
  const idb = te.encode(id)
  const info = new Uint8Array(14 + 4 + dom.length + 4 + idb.length)
  let o = 0
  info.set(te.encode('vulos-content:'), o); o += 14
  const dv = new DataView(info.buffer)
  dv.setUint32(o, dom.length); o += 4
  info.set(dom, o); o += dom.length
  dv.setUint32(o, idb.length); o += 4
  info.set(idb, o); o += idb.length

  const hkdfKey = await subtle().importKey('raw', masterKey, 'HKDF', false, ['deriveBits'])
  const bits = await subtle().deriveBits(
    { name: 'HKDF', hash: 'SHA-256', salt: ZERO_SALT_32, info },
    hkdfKey,
    256,
  )
  return new Uint8Array(bits)
}

// ─── In-memory master-key holder ──────────────────────────────────────────────
// The unlocked master key is kept ONLY in this module-level variable for the life
// of the tab. It is deliberately never placed in localStorage/sessionStorage/
// IndexedDB/cookies and never logged. Cleared on lock/logout via clearMasterKey().

let _masterKey = null

export function holdMasterKey(mk) {
  if (!(mk instanceof Uint8Array) || mk.length !== 32) {
    throw new Error('masterKey: refusing to hold a non-32-byte key')
  }
  _masterKey = mk
}

export function getMasterKey() {
  return _masterKey
}

export function hasMasterKey() {
  return _masterKey !== null
}

export function clearMasterKey() {
  if (_masterKey) _masterKey.fill(0)
  _masterKey = null
}

// unlockMasterKeyForSession fetches the password-wrapped envelope for the logged-
// in user and unwraps it client-side, holding the key in memory. Returns true on
// success, false if the account has no master key (legacy) — callers should treat
// a thrown error (wrong password / tamper) as fail-closed.
export async function unlockMasterKeyForSession(password, fetchImpl = fetch) {
  const res = await fetchImpl('/api/auth/masterkey/envelope')
  if (res.status === 404) return false // legacy account without a master key
  if (!res.ok) throw new Error(`masterKey: envelope fetch failed (${res.status})`)
  const slot = await res.json()
  const mk = await unwrapMasterKeyWithPassword(slot, password)
  holdMasterKey(mk)
  return true
}
