/**
 * pow.js — client-side proof-of-work (sha256-hashcash) for the control
 * plane's DDoS CaptchaGate (backend/cp/internal/ddos/captcha.go).
 *
 * The gate protects a handful of POST auth routes (login, signup,
 * forgot-password). Without this client the SPA cannot authenticate AT ALL:
 * the gate 403s every POST that lacks a valid X-Vulos-PoW header. Flow:
 *
 *   1. POST fails 403 with { error: "proof-of-work required", captcha_needed: "true" }
 *   2. GET /api/captcha/challenge?route=<path> → { challenge, difficulty }
 *   3. find a nonce so SHA256(challenge + nonce) has `difficulty` leading zero bits
 *   4. retry the POST once with  X-Vulos-PoW: "<challenge>:<nonce>"
 *
 * Challenges are single-use server-side (consumed on verify) and expire after
 * 10 minutes; difficulty is adaptive (4 bits base, up to 20 under attack), so
 * solving stays sub-second for legitimate users and the loop needs no cap.
 */

const CHALLENGE_URL = '/api/captcha/challenge'

/* True when the first `bits` bits of `bytes` are all zero — mirrors the
   backend's verifyHashcash bit-for-bit. */
export function hasLeadingZeroBits(bytes, bits) {
  for (let i = 0; i < bits; i++) {
    const byte = bytes[i >> 3]
    if (byte === undefined) return false
    if ((byte >> (7 - (i % 8))) & 1) return false
  }
  return true
}

/* Brute-force the nonce for one challenge. Returns the ready-to-send header
   value "<challenge>:<nonce>". WebCrypto digests are async, so every attempt
   naturally yields to the event loop — the UI never freezes. */
export async function solveChallenge(challenge, difficulty) {
  const enc = new TextEncoder()
  for (let nonce = 0; ; nonce++) {
    const digest = new Uint8Array(
      await crypto.subtle.digest('SHA-256', enc.encode(challenge + nonce)),
    )
    if (hasLeadingZeroBits(digest, difficulty)) return `${challenge}:${nonce}`
  }
}

/* Fetch a fresh challenge for `route` and solve it. Returns the header value,
   or null when the challenge endpoint is unavailable/malformed (callers fall
   back to surfacing the original 403). */
export async function solvePow(route, fetchImpl = fetch) {
  let res
  try {
    res = await fetchImpl(`${CHALLENGE_URL}?route=${encodeURIComponent(route)}`, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
  } catch {
    return null
  }
  if (!res.ok) return null
  const body = await res.json().catch(() => null)
  if (!body || typeof body.challenge !== 'string' || typeof body.difficulty !== 'number') {
    return null
  }
  return solveChallenge(body.challenge, body.difficulty)
}

/* True when a response body is the CaptchaGate's "solve a PoW first" shape. */
export function needsPow(status, body) {
  return (
    status === 403 &&
    !!body &&
    (body.captcha_needed === 'true' || body.captcha_needed === true)
  )
}

/* fetch() that transparently satisfies the CaptchaGate: on a PoW-403 it
   solves a challenge for the request path and retries ONCE with the
   X-Vulos-PoW header. Anything else (other errors, second 403) passes
   through untouched so callers keep their normal error handling. */
export async function powFetch(url, init = {}, fetchImpl = fetch) {
  const res = await fetchImpl(url, init)
  if (res.status !== 403) return res

  let body = null
  try {
    body = await res.clone().json()
  } catch {
    /* non-JSON 403 — not the gate */
  }
  if (!needsPow(res.status, body)) return res

  const route = new URL(url, globalThis.location?.origin ?? 'http://localhost').pathname
  const pow = await solvePow(route, fetchImpl)
  if (!pow) return res

  const headers = { ...(init.headers || {}), 'X-Vulos-PoW': pow }
  return fetchImpl(url, { ...init, headers })
}
