// AppBridge.js — ORIGIN-01: the shell side of the shell↔app postMessage bridge.
//
// WHY THIS EXISTS
//
// Apps no longer run on the shell's origin, so they can no longer touch the
// shell's localStorage — that is the entire point of ORIGIN-01. The five
// first-party apps that used to be granted `allow-same-origin` (calculator,
// clock, pdf-viewer, text-editor, weather) needed it for exactly one capability:
// reading/writing localStorage at load. They get that back here, and ONLY that.
//
// CAPABILITY SURFACE (v1) — the complete list. There is nothing else.
//
//   app → shell   vulos.bridge.hello      handshake; carries a MessagePort
//   app → shell   vulos.storage.set       {key, value}
//   app → shell   vulos.storage.remove    {key}
//   app → shell   vulos.storage.clear     (this app's namespace only)
//   shell → app   vulos.storage.snapshot  {data}  this app's keys only
//   app → shell   vulos.offline.state     {id}  → reply {unlocked}   (OFFLINE-AUTH-01)
//   app → shell   vulos.offline.appKey    {id}  → reply {key}  this app's per-app CryptoKey
//
// No DOM access. No navigation. No cookie access. No network proxying. No way to
// name another app. The app never supplies its own identity: the shell derives it
// from the FRAME the message arrived on, and every key is scoped to that identity
// before it touches storage. An app therefore cannot address another app's data
// or the shell's own keys even if it lies about everything in the message.
//
// OFFLINE-AUTH-01: an app can ask whether the OS offline session is unlocked (to
// gate its cached UI) and request ITS OWN per-app offline key (to encrypt its
// cache). The key is derived from the TRUSTED appId (the frame identity above),
// NEVER an appId the app names — this is the precondition that makes the per-app
// isolation real: an app can only ever obtain its own key. The CryptoKey is
// non-extractable and sent over the point-to-point MessagePort (structured clone
// preserves non-extractability), so the app can encrypt/decrypt but never export
// the raw bytes, and no key material crosses `postMessage(*)`.
//
// TRUST MODEL — two independent checks, both required, on every inbound message:
//
//   1. FRAME IDENTITY.  event.source must be the exact contentWindow of the
//      iframe we opened for this app. This is an object identity comparison
//      against a window handle we hold; it cannot be forged or spoofed by any
//      page, and it is what actually distinguishes "our app frame" from "some
//      other frame on the page" when the app runs opaque (where every frame
//      reports the same origin string, 'null').
//
//   2. ORIGIN.  event.origin must equal the origin we expect for this app: its
//      own origin when per-app origins are on, or the literal 'null' for an
//      opaque frame. This is what stops a cross-origin page from being mistaken
//      for the app once app origins are in play.
//
// Replies never use postMessage(…, '*'): after the handshake the shell only ever
// writes to the MessagePort the app handed it, which is point-to-point and
// unforgeable. The one message the APP sends to establish the channel is
// addressed to the shell's exact origin (see the injected client in
// backend/services/gateway/origin_bridge.go), so if the app's parent is not the
// shell the browser drops it and the bridge simply never forms. `*` appears
// nowhere in this protocol, in either direction.

import { expectedFrameOrigin } from './AppOrigins'
import { isUnlocked as offlineIsUnlocked, appKey as offlineAppKey } from '../lib/offlineAuth.js'

export const BRIDGE_VERSION = 1

// Namespace for app-owned data inside the shell's localStorage. The separator
// cannot occur in an app id (ids are [a-z0-9-]+), so the split is unambiguous.
const PREFIX = 'vulos.appdata.'
const SEP = '::'

// Quotas. An app frame is untrusted: without a cap it could fill the shell's
// localStorage and brick the desktop for every other app (the shell's own
// settings live in the same store). These are generous for preferences and
// notes — the actual use — and cheap to raise later.
const MAX_KEY_LEN = 512
const MAX_VALUE_LEN = 64 * 1024
const MAX_APP_BYTES = 256 * 1024

// The seed snapshot is passed to the frame in its URL fragment so the app can
// read it SYNCHRONOUSLY at first script execution (a postMessage handshake is
// async, and these apps read localStorage at load). Fragments are not sent to the
// server and not logged. Anything over the cap is delivered asynchronously over
// the port instead of bloating the URL.
const MAX_SEED_BYTES = 32 * 1024

function validAppId(appId) {
  return typeof appId === 'string' && /^[a-z0-9][a-z0-9-]*$/.test(appId)
}

function nsPrefix(appId) {
  return `${PREFIX}${appId}${SEP}`
}

function store() {
  try {
    return globalThis.localStorage || null
  } catch {
    return null
  }
}

// readAppSnapshot returns every key this app owns, unprefixed.
//
// The map has a NULL prototype on purpose. App keys are untrusted strings, and a
// plain {} would route a key named "__proto__" into the prototype setter rather
// than storing it (and "constructor"/"toString" would collide with inherited
// members on read). A null-prototype object has no such keys to confuse.
export function readAppSnapshot(appId) {
  const ls = store()
  const out = Object.create(null)
  if (!ls || !validAppId(appId)) return out
  const p = nsPrefix(appId)
  try {
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i)
      if (typeof k === 'string' && k.startsWith(p)) {
        const v = ls.getItem(k)
        if (typeof v === 'string') out[k.slice(p.length)] = v
      }
    }
  } catch { /* storage unavailable — empty snapshot is a safe answer */ }
  return out
}

function appBytes(appId) {
  const snap = readAppSnapshot(appId)
  let n = 0
  for (const k of Object.keys(snap)) n += k.length + snap[k].length
  return n
}

// writeAppItem persists one key inside the app's namespace, enforcing quotas.
// Returns false when refused; a refusal is silent to the app (it keeps its own
// in-memory copy) — we do not hand an untrusted frame a signal it could use to
// probe the shell's storage state.
export function writeAppItem(appId, key, value) {
  const ls = store()
  if (!ls || !validAppId(appId)) return false
  if (typeof key !== 'string' || typeof value !== 'string') return false
  if (key.length === 0 || key.length > MAX_KEY_LEN) return false
  if (value.length > MAX_VALUE_LEN) return false

  const full = nsPrefix(appId) + key
  let existing = 0
  try {
    const prev = ls.getItem(full)
    if (typeof prev === 'string') existing = key.length + prev.length
  } catch { /* ignore */ }
  if (appBytes(appId) - existing + key.length + value.length > MAX_APP_BYTES) return false

  try {
    ls.setItem(full, value)
    return true
  } catch {
    return false
  }
}

export function removeAppItem(appId, key) {
  const ls = store()
  if (!ls || !validAppId(appId) || typeof key !== 'string') return false
  try {
    ls.removeItem(nsPrefix(appId) + key)
    return true
  } catch {
    return false
  }
}

export function clearAppData(appId) {
  const ls = store()
  if (!ls || !validAppId(appId)) return false
  const p = nsPrefix(appId)
  try {
    const doomed = []
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i)
      if (typeof k === 'string' && k.startsWith(p)) doomed.push(k)
    }
    for (const k of doomed) ls.removeItem(k)
    return true
  } catch {
    return false
  }
}

// clearAllAppData drops EVERY app's bridge-stored data (all namespaces under the
// shell prefix). Used by the offline-auth wipe (OFFLINE-AUTH-01) so a credential
// wipe actually erases the app data the shell holds. It does NOT reach an app's
// own-origin IndexedDB/caches — only the app can clear those (on the wipe event);
// this clears the shell-owned copy, which is the part the OS can guarantee.
export function clearAllAppData() {
  const ls = store()
  if (!ls) return false
  try {
    const doomed = []
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i)
      if (typeof k === 'string' && k.startsWith(PREFIX)) doomed.push(k)
    }
    for (const k of doomed) ls.removeItem(k)
    return true
  } catch {
    return false
  }
}

// encodeSeed renders a snapshot for the iframe URL fragment. Returns '' when the
// snapshot is empty or too large to seed (the app then hydrates over the port).
export function encodeSeed(snapshot) {
  const keys = Object.keys(snapshot || {})
  if (keys.length === 0) return ''
  try {
    const json = JSON.stringify(snapshot)
    const bytes = new TextEncoder().encode(json)
    if (bytes.length > MAX_SEED_BYTES) return ''
    let bin = ''
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i])
    return encodeURIComponent(btoa(bin))
  } catch {
    return ''
  }
}

// appFrameSrc appends the seed fragment to an app frame URL.
export function appFrameSrc(url, appId) {
  if (!validAppId(appId)) return url
  const seed = encodeSeed(readAppSnapshot(appId))
  if (!seed) return url
  return `${url}${url.includes('#') ? '&' : '#'}__vulos_s=${seed}`
}

// handleBridgeMessage applies one port message. Exported so tests can drive the
// protocol directly without a real iframe. `reply`, when provided, posts a
// response back over the app's MessagePort (used by the request/response offline
// verbs); the storage verbs are fire-and-forget and ignore it. Returns a boolean
// "handled" synchronously even for the async offline.appKey verb (whose reply
// arrives later over the port), so existing callers/tests keep their contract.
export function handleBridgeMessage(appId, msg, reply) {
  if (!msg || typeof msg !== 'object') return false
  if (msg.v !== BRIDGE_VERSION) return false
  switch (msg.type) {
    case 'vulos.storage.set':
      return writeAppItem(appId, msg.key, msg.value)
    case 'vulos.storage.remove':
      return removeAppItem(appId, msg.key)
    case 'vulos.storage.clear':
      return clearAppData(appId)
    case 'vulos.offline.state':
      // Synchronous: does the OS offline session hold a key right now?
      if (typeof reply === 'function') {
        reply({ v: BRIDGE_VERSION, type: 'vulos.offline.state.reply', id: msg.id, unlocked: offlineIsUnlocked() })
      }
      return true
    case 'vulos.offline.appKey':
      // Async: derive THIS app's per-app key (from the trusted appId) and reply
      // over the port. Locked → key null. Never throws to the caller.
      replyOfflineAppKey(appId, msg.id, reply)
      return true
    default:
      // Unknown verb — the surface is closed, not extensible by the app.
      return false
  }
}

// replyOfflineAppKey derives the app's non-extractable per-app CryptoKey and posts
// it back over the port. `appId` is the TRUSTED frame identity, so an app can only
// ever receive its own key. Failure/locked yields key:null, never an exception.
function replyOfflineAppKey(appId, id, reply) {
  if (typeof reply !== 'function') return
  const send = (key) => reply({ v: BRIDGE_VERSION, type: 'vulos.offline.appKey.reply', id, key })
  if (!offlineIsUnlocked()) { send(null); return }
  offlineAppKey(appId).then(send, () => send(null))
}

// attachAppBridge wires an app iframe to the shell. Returns a detach function.
//
// frameUrl is the URL the iframe was given: it determines the origin we will
// accept messages from, so it must be the same string handed to the <iframe>.
export function attachAppBridge(iframeEl, { appId, frameUrl }) {
  if (!iframeEl || !validAppId(appId)) return () => {}
  const expectedOrigin = expectedFrameOrigin(frameUrl)
  let port = null

  const onMessage = (e) => {
    // CHECK 1 — frame identity. Unforgeable; the only check that means anything
    // when the frame is opaque (every opaque frame's origin is the string 'null').
    if (!iframeEl.contentWindow || e.source !== iframeEl.contentWindow) return

    // CHECK 2 — origin. The app's own origin, or 'null' for an opaque frame.
    if (e.origin !== expectedOrigin) return

    const msg = e.data
    if (!msg || typeof msg !== 'object') return
    if (msg.type !== 'vulos.bridge.hello' || msg.v !== BRIDGE_VERSION) return

    const p = e.ports && e.ports[0]
    if (!p) return

    // One channel per frame. A second hello (e.g. after the app navigates itself)
    // replaces the old port; the old one is dropped.
    if (port) { try { port.close() } catch { /* already gone */ } }
    port = p
    port.onmessage = (ev) => {
      handleBridgeMessage(appId, ev.data, (obj) => {
        try { port.postMessage(obj) } catch { /* frame went away */ }
      })
    }
    // Reply on the PORT — never postMessage(..., '*').
    try {
      port.postMessage({
        v: BRIDGE_VERSION,
        type: 'vulos.storage.snapshot',
        data: readAppSnapshot(appId),
      })
    } catch { /* frame went away */ }
  }

  window.addEventListener('message', onMessage)
  return () => {
    window.removeEventListener('message', onMessage)
    if (port) { try { port.close() } catch { /* already gone */ } }
    port = null
  }
}
