// webPush.js — client opt-in for the cell-side Web Push send-path (PUSH-CELL-01).
//
// The box (cell) sends VAPID Web Push DIRECTLY to the browser-vendor push
// service; this module is the browser half: it fetches the cell's VAPID PUBLIC
// key, subscribes through the already-registered service worker's PushManager,
// and registers the resulting subscription with the box (POST
// /api/notifications/push/subscribe). The box then encrypts each notification to
// this subscription's keys (RFC 8291) — the vendor routes but cannot read.
//
// Entirely OPT-IN and best-effort: every failure path returns a falsy result
// rather than throwing, so a browser without push support, a box without VAPID
// keys, or a user who denies permission simply gets no Web Push (the in-app
// WebSocket notification stream is unaffected). No secret ever touches this code
// — only the PUBLIC key does.

// urlBase64ToUint8Array converts the base64url VAPID public key into the
// Uint8Array that PushManager.subscribe expects as applicationServerKey.
export function urlBase64ToUint8Array(base64String) {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(base64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

// pushSupported reports whether this browser exposes the APIs we need.
export function pushSupported() {
  return (
    typeof navigator !== 'undefined' &&
    'serviceWorker' in navigator &&
    typeof PushManager !== 'undefined' &&
    typeof Notification !== 'undefined'
  )
}

// enableWebPush opts the current device into Web Push. Returns the stored
// subscription on success, or null on any non-fatal failure (unsupported,
// disabled on the box, permission denied, network error). Safe to call more
// than once — PushManager.subscribe is idempotent for the same key, and the box
// upserts by endpoint.
//
// deps is injectable for tests: { fetch, registrationPromise, requestPermission }.
export async function enableWebPush(deps = {}) {
  const doFetch = deps.fetch || (typeof fetch !== 'undefined' ? fetch : null)
  if (!doFetch || !pushSupported()) return null

  // 1) Ask the box for its VAPID public key + whether push is configured.
  let vapid
  try {
    const res = await doFetch('/api/notifications/push/vapid-public', {
      headers: { Accept: 'application/json' },
    })
    if (!res || !res.ok) return null
    vapid = await res.json()
  } catch {
    return null
  }
  if (!vapid || !vapid.enabled || !vapid.publicKey) return null // fail-safe-off

  // 2) Ensure notification permission (never nag: only prompt when 'default').
  try {
    const requestPermission =
      deps.requestPermission || (() => Notification.requestPermission())
    let perm = Notification.permission
    if (perm === 'default') perm = await requestPermission()
    if (perm !== 'granted') return null
  } catch {
    return null
  }

  // 3) Subscribe through the registered service worker's PushManager.
  let subscription
  try {
    const reg =
      deps.registrationPromise ||
      (navigator.serviceWorker.ready)
    const registration = await reg
    subscription =
      (await registration.pushManager.getSubscription()) ||
      (await registration.pushManager.subscribe({
        userVisibleOnly: true, // required by Chromium; we always show the push
        applicationServerKey: urlBase64ToUint8Array(vapid.publicKey),
      }))
  } catch {
    return null
  }
  if (!subscription) return null

  // 4) Register the subscription with the box. The box stamps the owner from
  //    the authenticated session — the body carries only endpoint + keys.
  try {
    const body = subscription.toJSON ? subscription.toJSON() : subscription
    const res = await doFetch('/api/notifications/push/subscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint: body.endpoint, keys: body.keys }),
    })
    if (!res || !res.ok) return null
  } catch {
    return null
  }
  return subscription
}

// disableWebPush unsubscribes this device and tells the box to forget it. Best
// effort; returns true when the box acknowledged the removal.
export async function disableWebPush(deps = {}) {
  const doFetch = deps.fetch || (typeof fetch !== 'undefined' ? fetch : null)
  if (!doFetch || !pushSupported()) return false
  try {
    const reg = deps.registrationPromise || navigator.serviceWorker.ready
    const registration = await reg
    const subscription = await registration.pushManager.getSubscription()
    if (!subscription) return true // already gone
    const endpoint = subscription.endpoint
    await subscription.unsubscribe().catch(() => {})
    const res = await doFetch('/api/notifications/push/subscribe', {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint }),
    })
    return !!(res && res.ok)
  } catch {
    return false
  }
}
