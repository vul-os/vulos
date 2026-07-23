/**
 * passkey.js — WebAuthn (FIDO2 / passkey) enrollment + sign-in for the Vulos
 * cloud console.
 *
 * Passkeys are an ADDITIONAL authenticator alongside password + OAuth + TOTP
 * (backend: internal/auth/webauthn.go). This module is the browser half of the
 * four CP endpoints:
 *
 *   enroll : POST /api/auth/webauthn/register/begin  (session) → CredentialCreation
 *            navigator.credentials.create(...)
 *            POST /api/auth/webauthn/register/finish?name=… (session)
 *
 *   signin : POST /api/auth/webauthn/login/begin  {email} → { user_id, options }
 *            navigator.credentials.get(...)
 *            POST /api/auth/webauthn/login/finish?user_id=…  → sets vc_session
 *
 * The server speaks the standard WebAuthn JSON dialect (go-webauthn): challenges
 * and credential ids are base64url (no padding), and the create()/get() results
 * must be re-encoded to base64url before POSTing. All conversions live in the
 * pure helpers below so they are unit-testable without a real authenticator;
 * `fetch` and the `credentials` container (navigator.credentials) are injectable
 * exactly like pow.js injects fetch.
 */

/* ─── base64url ⇄ bytes (RFC 4648 §5, no padding) ───────────────────────── */

export function b64urlToBytes(s) {
  const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4))
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + pad
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export function bytesToB64url(buf) {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let bin = ''
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/* True when this browser can do WebAuthn at all. */
export function passkeySupported(nav = typeof navigator !== 'undefined' ? navigator : undefined) {
  return !!(nav && nav.credentials && typeof nav.credentials.create === 'function' &&
    typeof nav.credentials.get === 'function' && typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential !== 'undefined')
}

/* ─── options JSON → PublicKeyCredential*Options (ArrayBuffers) ──────────── */

// The server sends { publicKey: { challenge, user?, allowCredentials?, excludeCredentials?, ... } }.
// Decode the base64url fields the DOM API needs as BufferSource in place.
export function decodePublicKeyOptions(options) {
  const pk = options && options.publicKey ? { ...options.publicKey } : { ...options }
  if (typeof pk.challenge === 'string') pk.challenge = b64urlToBytes(pk.challenge)
  if (pk.user && typeof pk.user.id === 'string') {
    pk.user = { ...pk.user, id: b64urlToBytes(pk.user.id) }
  }
  const decodeList = (list) =>
    Array.isArray(list)
      ? list.map((c) => ({ ...c, id: typeof c.id === 'string' ? b64urlToBytes(c.id) : c.id }))
      : list
  if (pk.allowCredentials) pk.allowCredentials = decodeList(pk.allowCredentials)
  if (pk.excludeCredentials) pk.excludeCredentials = decodeList(pk.excludeCredentials)
  return pk
}

/* ─── PublicKeyCredential → WebAuthn JSON (base64url) ────────────────────── */

export function encodeRegistrationCredential(cred) {
  const r = cred.response
  const out = {
    id: cred.id,
    rawId: bytesToB64url(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: bytesToB64url(r.attestationObject),
      clientDataJSON: bytesToB64url(r.clientDataJSON),
    },
    clientExtensionResults:
      typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
  }
  if (typeof r.getTransports === 'function') {
    const t = r.getTransports()
    if (Array.isArray(t) && t.length) out.transports = t
  }
  return out
}

export function encodeAssertionCredential(cred) {
  const r = cred.response
  return {
    id: cred.id,
    rawId: bytesToB64url(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: bytesToB64url(r.authenticatorData),
      clientDataJSON: bytesToB64url(r.clientDataJSON),
      signature: bytesToB64url(r.signature),
      userHandle: r.userHandle ? bytesToB64url(r.userHandle) : null,
    },
    clientExtensionResults:
      typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
  }
}

/* ─── HTTP helpers ──────────────────────────────────────────────────────── */

async function readJSON(res) {
  const text = await res.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

function errorFrom(res, body, fallback) {
  const msg =
    (body && (body.error || body.message)) ||
    (res.status === 429 ? 'Too many attempts — try again shortly.' : fallback)
  const err = new Error(msg)
  err.status = res.status
  return err
}

/* ─── Public flows ──────────────────────────────────────────────────────── */

/**
 * enrollPasskey — register a new passkey on the CURRENT (already logged-in)
 * account. Requires a live session cookie. `name` is a user-facing label.
 * Injectables (deps): { fetch, credentials } default to the globals.
 */
export async function enrollPasskey(name = '', deps = {}) {
  const doFetch = deps.fetch || fetch
  const creds = deps.credentials || (typeof navigator !== 'undefined' ? navigator.credentials : undefined)
  if (!creds) throw new Error('This browser does not support passkeys.')

  const beginRes = await doFetch('/api/auth/webauthn/register/begin', {
    method: 'POST',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  const beginBody = await readJSON(beginRes)
  if (!beginRes.ok) throw errorFrom(beginRes, beginBody, 'Could not start passkey enrollment.')

  const publicKey = decodePublicKeyOptions(beginBody)
  const cred = await creds.create({ publicKey })
  if (!cred) throw new Error('Passkey enrollment was cancelled.')

  const q = name ? `?name=${encodeURIComponent(name)}` : ''
  const finishRes = await doFetch(`/api/auth/webauthn/register/finish${q}`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(encodeRegistrationCredential(cred)),
  })
  const finishBody = await readJSON(finishRes)
  if (!finishRes.ok) throw errorFrom(finishRes, finishBody, 'Passkey enrollment failed.')
  return finishBody // the created WebAuthnCredential record
}

/**
 * signInWithPasskey — authenticate with a passkey for `email`. On success the
 * server sets the vc_session cookie and this resolves; the caller then refreshes
 * the auth context. Throws with a human message otherwise.
 */
export async function signInWithPasskey(email, deps = {}) {
  const doFetch = deps.fetch || fetch
  const creds = deps.credentials || (typeof navigator !== 'undefined' ? navigator.credentials : undefined)
  if (!creds) throw new Error('This browser does not support passkeys.')
  if (!email) throw new Error('Enter your Vulos address first.')

  const beginRes = await doFetch('/api/auth/webauthn/login/begin', {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ email }),
  })
  const beginBody = await readJSON(beginRes)
  if (!beginRes.ok) throw errorFrom(beginRes, beginBody, 'No passkeys registered for this account.')
  if (!beginBody || !beginBody.user_id || !beginBody.options) {
    throw new Error('Unexpected passkey challenge from server.')
  }

  const publicKey = decodePublicKeyOptions(beginBody.options)
  const assertion = await creds.get({ publicKey })
  if (!assertion) throw new Error('Passkey sign-in was cancelled.')

  const finishRes = await doFetch(
    `/api/auth/webauthn/login/finish?user_id=${encodeURIComponent(beginBody.user_id)}`,
    {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(encodeAssertionCredential(assertion)),
    },
  )
  const finishBody = await readJSON(finishRes)
  if (!finishRes.ok) throw errorFrom(finishRes, finishBody, 'Passkey sign-in failed.')
  return finishBody
}
