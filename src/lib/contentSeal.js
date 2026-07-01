// contentSeal.js — CLIENT-SIDE content-blind file sharing (WAVE-3).
//
// This is the browser half of the "VSEAL1" sealed-content envelope defined by
// backend/services/files/contentseal.go. When a user shares a file with an
// account-only (cloud) recipient, the sharer's browser SEALS the bytes here so the
// Vulos cell that relays them can never read the content: the content key is
// wrapped to the recipient's PUBLISHED X25519 content key (and to the sharer's own,
// so the sharer keeps access). The recipient's browser OPENS it here with its
// master key. The cell only ever transports ciphertext.
//
// Zero third-party JS: everything uses the platform WebCrypto SubtleCrypto, exactly
// like src/lib/masterKey.js. That constrains the primitives to those WebCrypto
// offers — hence AES-256-GCM (not XChaCha20-Poly1305) as the AEAD; the wire format
// is chosen for Go<->WebCrypto parity and is asserted byte-for-byte by
// src/lib/__tests__/contentSeal.test.js against a Go-generated fixture.
//
// WIRE FORMAT (must match contentseal.go):
//   magic  "VSEAL1\n" (7 bytes)
//   hdrLen uint32 big-endian
//   header JSON { v, aead:"AES-256-GCM", kdf:"HKDF-SHA256", kem:"X25519",
//                 cnonce:<b64 12B>, wraps:[{kid,epk,wnonce,wct}] }
//   body   AES-256-GCM(plaintext, CEK, cnonce, aad="vulos-seal-content-v1")
//
// Fail-closed: every AEAD open authenticates; wrong key / tamper throws.

const MAGIC = 'VSEAL1\n'
const VERSION = 1
const AEAD = 'AES-256-GCM'
const KDF = 'HKDF-SHA256'
const KEM = 'X25519'

const CONTENT_AAD = 'vulos-seal-content-v1'
const WRAP_AAD = 'vulos-seal-wrap-v1'
const WRAP_INFO = 'vulos-seal-wrap-v1'
const CONTENT_KEY_INFO = 'vulos-content-x25519-v1'

const ZERO_SALT_32 = new Uint8Array(32)
const GCM_NONCE_LEN = 12
const X25519_LEN = 32

// PKCS8 prefix that wraps a raw 32-byte X25519 scalar so WebCrypto importKey can
// ingest it as an X25519 private key (WebCrypto has no "raw" X25519 private import).
const PKCS8_X25519_PREFIX = Uint8Array.from([
  0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20,
])

const te = new TextEncoder()

function subtle() {
  const s = globalThis.crypto && globalThis.crypto.subtle
  if (!s) throw new Error('contentSeal: WebCrypto SubtleCrypto unavailable (requires a secure context)')
  return s
}

// ─── base64 (std, padded — matches Go encoding/base64.StdEncoding) ─────────────

function bytesToB64(bytes) {
  let bin = ''
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
  return btoa(bin)
}

function b64ToBytes(b64) {
  const bin = atob(String(b64))
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

function concatBytes(...arrs) {
  let n = 0
  for (const a of arrs) n += a.length
  const out = new Uint8Array(n)
  let o = 0
  for (const a of arrs) {
    out.set(a, o)
    o += a.length
  }
  return out
}

// ─── primitives ────────────────────────────────────────────────────────────────

// hkdf32 = HKDF-SHA256(ikm, salt=zero32, info) -> Uint8Array(32).
async function hkdf32(ikm, info) {
  const key = await subtle().importKey('raw', ikm, 'HKDF', false, ['deriveBits'])
  const bits = await subtle().deriveBits(
    { name: 'HKDF', hash: 'SHA-256', salt: ZERO_SALT_32, info },
    key,
    256,
  )
  return new Uint8Array(bits)
}

// importX25519Priv imports a raw 32-byte scalar as an X25519 private CryptoKey.
async function importX25519Priv(scalar32) {
  const pkcs8 = concatBytes(PKCS8_X25519_PREFIX, scalar32)
  return subtle().importKey('pkcs8', pkcs8, { name: 'X25519' }, false, ['deriveBits'])
}

async function importX25519Pub(raw32) {
  return subtle().importKey('raw', raw32, { name: 'X25519' }, false, [])
}

// x25519 computes the 32-byte ECDH result of privKey against a raw public key.
async function x25519(privKey, pubRaw32) {
  const pub = await importX25519Pub(pubRaw32)
  const bits = await subtle().deriveBits({ name: 'X25519', public: pub }, privKey, 256)
  return new Uint8Array(bits)
}

const X25519_BASEPOINT = (() => {
  const b = new Uint8Array(32)
  b[0] = 9
  return b
})()

async function aesGcmSeal(key32, nonce12, plaintext, aadStr) {
  const key = await subtle().importKey('raw', key32, 'AES-GCM', false, ['encrypt'])
  const ct = await subtle().encrypt(
    { name: 'AES-GCM', iv: nonce12, additionalData: te.encode(aadStr) },
    key,
    plaintext,
  )
  return new Uint8Array(ct)
}

async function aesGcmOpen(key32, nonce12, ct, aadStr) {
  const key = await subtle().importKey('raw', key32, 'AES-GCM', false, ['decrypt'])
  const pt = await subtle().decrypt(
    { name: 'AES-GCM', iv: nonce12, additionalData: te.encode(aadStr) },
    key,
    ct,
  )
  return new Uint8Array(pt)
}

function randomBytes(n) {
  return globalThis.crypto.getRandomValues(new Uint8Array(n))
}

// ─── content keypair (derived from the master key) ────────────────────────────

// deriveContentKeyPair derives the user's X25519 CONTENT keypair from their master
// key. The public key (base64) is what gets published to the directory
// (content_pubkey_b64) and what sharers wrap to. Mirrors DeriveContentX25519 in Go.
// Returns { priv: CryptoKey, pub: Uint8Array(32), pubB64: string }.
export async function deriveContentKeyPair(masterKey) {
  if (!(masterKey instanceof Uint8Array) || masterKey.length !== 32) {
    throw new Error('contentSeal: master key must be 32 bytes')
  }
  const scalar = await hkdf32(masterKey, te.encode(CONTENT_KEY_INFO))
  const priv = await importX25519Priv(scalar)
  const pub = await x25519(priv, X25519_BASEPOINT)
  scalar.fill(0)
  return { priv, pub, pubB64: bytesToB64(pub) }
}

// deriveContentPubKeyB64 returns just the published public content key (base64).
export async function deriveContentPubKeyB64(masterKey) {
  const { pubB64 } = await deriveContentKeyPair(masterKey)
  return pubB64
}

async function wrapKeyFor(shared, recipientPubRaw) {
  const info = concatBytes(te.encode(WRAP_INFO), recipientPubRaw)
  return hkdf32(shared, info)
}

// ─── seal ──────────────────────────────────────────────────────────────────────

// seal encrypts plaintext (Uint8Array) under a fresh AES-256 content key and wraps
// that key to every recipient X25519 public key in recipientPubsB64 (base64 std).
// EVERY party who must retain access — including the SHARER — must be listed.
// Returns the VSEAL1 envelope as a Uint8Array. Throws (fail-closed) if no valid
// recipient key is given — a share is NEVER silently left as plaintext.
export async function seal(plaintext, recipientPubsB64) {
  if (!(plaintext instanceof Uint8Array)) plaintext = new Uint8Array(plaintext)
  if (!Array.isArray(recipientPubsB64) || recipientPubsB64.length === 0) {
    throw new Error('contentSeal: no recipient content keys (refusing to seal to nobody)')
  }
  const cek = randomBytes(32)
  const cnonce = randomBytes(GCM_NONCE_LEN)
  const body = await aesGcmSeal(cek, cnonce, plaintext, CONTENT_AAD)

  const wraps = []
  const seen = new Set()
  for (const kidB64 of recipientPubsB64) {
    if (!kidB64 || seen.has(kidB64)) continue
    seen.add(kidB64)
    const rpub = b64ToBytes(kidB64)
    if (rpub.length !== X25519_LEN) throw new Error('contentSeal: bad recipient content key')
    const eScalar = randomBytes(32)
    const ePriv = await importX25519Priv(eScalar)
    eScalar.fill(0)
    const epk = await x25519(ePriv, X25519_BASEPOINT)
    const shared = await x25519(ePriv, rpub)
    const wrapKey = await wrapKeyFor(shared, rpub)
    shared.fill(0)
    const wnonce = randomBytes(GCM_NONCE_LEN)
    const wct = await aesGcmSeal(wrapKey, wnonce, cek, WRAP_AAD)
    wrapKey.fill(0)
    wraps.push({
      kid: kidB64,
      epk: bytesToB64(epk),
      wnonce: bytesToB64(wnonce),
      wct: bytesToB64(wct),
    })
  }
  cek.fill(0)
  if (wraps.length === 0) throw new Error('contentSeal: no valid recipient content keys')

  const header = {
    v: VERSION,
    aead: AEAD,
    kdf: KDF,
    kem: KEM,
    cnonce: bytesToB64(cnonce),
    wraps,
  }
  const hdrBytes = te.encode(JSON.stringify(header))
  const lenPrefix = new Uint8Array(4)
  new DataView(lenPrefix.buffer).setUint32(0, hdrBytes.length, false) // big-endian
  return concatBytes(te.encode(MAGIC), lenPrefix, hdrBytes, body)
}

// ─── parse / open ────────────────────────────────────────────────────────────

// isSealed reports whether blob begins with the VSEAL1 magic.
export function isSealed(blob) {
  if (!(blob instanceof Uint8Array) || blob.length < MAGIC.length) return false
  for (let i = 0; i < MAGIC.length; i++) {
    if (blob[i] !== MAGIC.charCodeAt(i)) return false
  }
  return true
}

// parseSealed validates framing + header and returns { header, body }.
export function parseSealed(blob) {
  if (!isSealed(blob)) throw new Error('contentSeal: bad magic')
  let o = MAGIC.length
  if (blob.length < o + 4) throw new Error('contentSeal: truncated length')
  const hlen = new DataView(blob.buffer, blob.byteOffset + o, 4).getUint32(0, false)
  o += 4
  if (hlen === 0 || blob.length < o + hlen) throw new Error('contentSeal: truncated header')
  const hdrBytes = blob.subarray(o, o + hlen)
  const header = JSON.parse(new TextDecoder().decode(hdrBytes))
  if (header.v !== VERSION || header.aead !== AEAD || header.kem !== KEM) {
    throw new Error('contentSeal: unsupported version/alg')
  }
  if (!Array.isArray(header.wraps) || header.wraps.length === 0) {
    throw new Error('contentSeal: no wraps')
  }
  return { header, body: blob.subarray(o + hlen) }
}

// sealedKIDs returns the recipient key-ids a blob is wrapped to (content-blind).
export function sealedKIDs(blob) {
  const { header } = parseSealed(blob)
  return header.wraps.map((w) => w.kid)
}

// open decrypts a VSEAL1 envelope with the recipient's master key. Fail-closed:
// no matching wrap, a wrong key, or any tampering throws.
export async function open(blob, masterKey) {
  const { priv, pubB64 } = await deriveContentKeyPair(masterKey)
  const { header, body } = parseSealed(blob)
  const wrap = header.wraps.find((w) => w.kid === pubB64)
  if (!wrap) throw new Error('contentSeal: no wrap for this recipient')
  const epk = b64ToBytes(wrap.epk)
  if (epk.length !== X25519_LEN) throw new Error('contentSeal: bad epk')
  const shared = await x25519(priv, epk)
  const wrapKey = await wrapKeyFor(shared, b64ToBytes(pubB64))
  shared.fill(0)
  const cek = await aesGcmOpen(wrapKey, b64ToBytes(wrap.wnonce), b64ToBytes(wrap.wct), WRAP_AAD)
  wrapKey.fill(0)
  const cnonce = b64ToBytes(header.cnonce)
  const pt = await aesGcmOpen(cek, cnonce, body, CONTENT_AAD)
  cek.fill(0)
  return pt
}

// ─── sealed metadata container (VMETA1) ────────────────────────────────────────
//
// WAVE-7: the file name / content-type / is_dir are packed INSIDE the sealed body so
// the relaying cell (which only parses the VSEAL1 header) never sees them. Mirrors
// contentseal.go PackSealedPayload / UnpackSealedPayload byte-for-byte:
//
//   magic  "VMETA1\n" (7 bytes)
//   mlen   uint32 big-endian
//   meta   JSON { name, content_type, is_dir }
//   payload the file bytes (or, for is_dir, a tar archive)
//
// Back-compat: a plaintext without the magic is a legacy raw payload (meta = null).
const META_MAGIC = 'VMETA1\n'
const MAX_META = 1 << 16

// packMeta frames metadata ahead of payload for sealing. Pass meta=null to omit it.
export function packMeta(meta, payload) {
  if (!(payload instanceof Uint8Array)) payload = new Uint8Array(payload)
  if (!meta) return payload
  const metaJSON = te.encode(JSON.stringify({
    name: meta.name || '',
    content_type: meta.content_type || '',
    is_dir: !!meta.is_dir,
  }))
  if (metaJSON.length > MAX_META) throw new Error('contentSeal: sealed metadata too large')
  const lenPrefix = new Uint8Array(4)
  new DataView(lenPrefix.buffer).setUint32(0, metaJSON.length, false)
  return concatBytes(te.encode(META_MAGIC), lenPrefix, metaJSON, payload)
}

// unpackMeta splits a decrypted VMETA1 plaintext into { meta, payload }. A plaintext
// without the magic is returned as { meta: null, payload }. Fail-closed on a
// truncated/malformed frame.
export function unpackMeta(plaintext) {
  if (!(plaintext instanceof Uint8Array)) plaintext = new Uint8Array(plaintext)
  const magic = te.encode(META_MAGIC)
  if (plaintext.length < magic.length) return { meta: null, payload: plaintext }
  for (let i = 0; i < magic.length; i++) {
    if (plaintext[i] !== magic[i]) return { meta: null, payload: plaintext }
  }
  let o = magic.length
  if (plaintext.length < o + 4) throw new Error('contentSeal: truncated metadata length')
  const mlen = new DataView(plaintext.buffer, plaintext.byteOffset + o, 4).getUint32(0, false)
  o += 4
  if (mlen > MAX_META || plaintext.length < o + mlen) throw new Error('contentSeal: truncated metadata')
  const meta = JSON.parse(new TextDecoder().decode(plaintext.subarray(o, o + mlen)))
  return { meta, payload: plaintext.subarray(o + mlen) }
}

// ─── tar reader (untar sealed folders, zero third-party JS) ─────────────────────
//
// Reads the USTAR/PAX tar archive that vulos streamFolderTar (Go archive/tar) emits
// for a shared folder. Handles regular files, directories, and PAX 'x' extended
// headers (used for long "path=" names). Returns [{ name, isDir, bytes }].
function parseOctal(bytes) {
  let s = ''
  for (let i = 0; i < bytes.length; i++) {
    const c = bytes[i]
    if (c === 0 || c === 0x20) continue
    s += String.fromCharCode(c)
  }
  return s ? parseInt(s, 8) : 0
}

export function untar(buf) {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  const td = new TextDecoder()
  const out = []
  let off = 0
  let paxPath = null
  while (off + 512 <= bytes.length) {
    const header = bytes.subarray(off, off + 512)
    // Two consecutive zero blocks mark end-of-archive.
    let allZero = true
    for (let i = 0; i < 512; i++) { if (header[i] !== 0) { allZero = false; break } }
    if (allZero) break
    let name = td.decode(header.subarray(0, 100)).replace(/\0.*$/, '')
    const prefix = td.decode(header.subarray(345, 500)).replace(/\0.*$/, '')
    if (prefix) name = prefix + '/' + name
    const size = parseOctal(header.subarray(124, 136))
    const typeflag = String.fromCharCode(header[156] || 0x30)
    off += 512
    const data = bytes.subarray(off, off + size)
    off += Math.ceil(size / 512) * 512
    if (typeflag === 'x' || typeflag === 'g') {
      // PAX extended header: parse "len key=value\n" records; capture path override.
      const rec = td.decode(data)
      const m = rec.match(/\d+ path=([^\n]*)\n/)
      if (m) paxPath = m[1]
      continue
    }
    if (paxPath !== null) { name = paxPath; paxPath = null }
    const isDir = typeflag === '5' || name.endsWith('/')
    out.push({ name: name.replace(/\/+$/, ''), isDir, bytes: isDir ? new Uint8Array(0) : new Uint8Array(data) })
  }
  return out
}

// ─── directory publish ─────────────────────────────────────────────────────────

// publishContentPublicKey publishes the caller's derived content public key to
// their profile so peers can wrap file content to it. Idempotent (same master key
// -> same key). Returns the published base64 key. Throws on a non-2xx response so a
// failed publish is never silently ignored (a recipient with no published key
// cannot receive content-blind shares — fail closed on the sender side).
export async function publishContentPublicKey(masterKey, fetchImpl = fetch) {
  const pubB64 = await deriveContentPubKeyB64(masterKey)
  const res = await fetchImpl('/api/peering/profile', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content_pub_key: pubB64 }),
  })
  if (!res.ok) throw new Error(`contentSeal: publish content key failed (${res.status})`)
  return pubB64
}
