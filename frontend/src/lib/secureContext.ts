// secureContext.ts — shared guard for the src/lib modules that need WebCrypto
// SubtleCrypto (offlineAuth.js, masterKey.js, contentSeal.js, offline/index.js).
//
// WHY: `window.crypto.subtle` only exists in a browser "secure context" —
// https://, or http://localhost / http://127.0.0.1 / http://*.localhost (see
// MDN "Secure Contexts"). The shipped box is reached over plain HTTP on a LAN
// address (http://192.168.1.50) or an mDNS name (http://vulos.local), NEITHER
// of which qualifies, so `crypto.subtle` is `undefined` there and every call
// site would otherwise throw a raw "Cannot read properties of undefined
// (reading 'importKey')" TypeError with no indication of why or how to fix
// it. This module gives every crypto entry point one clear, accurate,
// non-alarmist error instead — it changes NO cryptographic behaviour, it only
// diagnoses the environment before the real crypto call would have failed
// anyway.
//
// Call requireSubtle() at each EXPORTED crypto entry point (not at module load
// time) so importing these modules never fails for a caller that doesn't
// happen to exercise a crypto path on this page load.

export const INSECURE_CONTEXT_MESSAGE =
  'This feature needs browser encryption (WebCrypto) that only runs on a ' +
  '"secure" connection. This page was opened over plain http:// on an address ' +
  "browsers don't treat as secure (a LAN IP or a .local name), so it isn't " +
  'available here — this is a browser restriction, not a problem with your ' +
  'data. Two ways to fix it: open the box over https:// (a self-signed ' +
  "certificate is enough — accept the browser's one-time warning and the " +
  'connection becomes secure), or open an SSH tunnel to the box and use ' +
  'http://localhost:<port>, which browsers also treat as secure.'

// A plain Error tagged with a `code` so callers can distinguish this guard
// from an unrelated failure without string-matching the message.
export type TaggedError = Error & { code: string }

// hasSubtleCrypto reports whether the platform SubtleCrypto implementation is
// available in this context, without throwing. Useful for callers that want
// to pre-flight a UI state (e.g. disable a button) rather than catch.
export function hasSubtleCrypto(): boolean {
  return !!(globalThis.crypto && globalThis.crypto.subtle)
}

// requireSubtle returns the platform SubtleCrypto implementation, or throws a
// tagged Error (code: 'INSECURE_CONTEXT') carrying INSECURE_CONTEXT_MESSAGE.
export function requireSubtle(): SubtleCrypto {
  const subtle = globalThis.crypto && globalThis.crypto.subtle
  if (subtle) return subtle
  const e: TaggedError = Object.assign(new Error(INSECURE_CONTEXT_MESSAGE), { code: 'INSECURE_CONTEXT' })
  throw e
}
