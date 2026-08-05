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

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

type MaybeRegistration = ServiceWorkerRegistration | null | undefined
type RegistrationSource = MaybeRegistration | Promise<MaybeRegistration>

// deps is injectable for tests: { fetch, registrationPromise, requestPermission }.
export interface WebPushDeps {
  fetch?: typeof fetch
  registrationPromise?: RegistrationSource
  requestPermission?: () => Promise<NotificationPermission> | NotificationPermission
  cpRegistrationPromise?: RegistrationSource
}

// urlBase64ToUint8Array converts the base64url VAPID public key into the
// Uint8Array that PushManager.subscribe expects as applicationServerKey.
export function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(base64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

// pushSupported reports whether this browser exposes the APIs we need.
export function pushSupported(): boolean {
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
export async function enableWebPush(deps: WebPushDeps = {}): Promise<PushSubscription | null> {
  const doFetch = deps.fetch || (typeof fetch !== 'undefined' ? fetch : null)
  if (!doFetch || !pushSupported()) return null

  // 1) Ask the box for its VAPID public key + whether push is configured.
  let vapid: unknown
  try {
    const res = await doFetch('/api/notifications/push/vapid-public', {
      headers: { Accept: 'application/json' },
    })
    if (!res || !res.ok) return null
    vapid = await res.json()
  } catch {
    return null
  }
  if (!isRecord(vapid) || !vapid.enabled || typeof vapid.publicKey !== 'string') return null // fail-safe-off

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
  let subscription: PushSubscription | null
  try {
    const reg =
      deps.registrationPromise ||
      (navigator.serviceWorker.ready)
    const registration = await reg
    // Mirrors disableWebPush's null-registration note: the original untyped
    // code let a null registration throw into the surrounding catch (→ null);
    // this makes that same outcome explicit.
    if (!registration) return null
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

  // 4) Register the CELL-KEYED subscription with the box (direct send path). The
  //    box stamps the owner from the authenticated session — body carries only
  //    endpoint + keys.
  try {
    // Read defensively (not through the native PushSubscriptionJSON type):
    // the toJSON guard exists so a test-injected mock subscription without a
    // .toJSON() still works, so `body` isn't guaranteed to structurally match
    // the real DOM shape either.
    const body: unknown = subscription.toJSON ? subscription.toJSON() : subscription
    const endpoint = isRecord(body) && typeof body.endpoint === 'string' ? body.endpoint : undefined
    const keys = isRecord(body) && isRecord(body.keys) ? body.keys : undefined
    const res = await doFetch('/api/notifications/push/subscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint, keys }),
    })
    if (!res || !res.ok) return null
  } catch {
    return null
  }

  // 5) SEND-ON-BEHALF (managed cells): ALSO create a SEPARATE, CP-KEYED
  //    subscription with the CP's OWN public VAPID key and forward it to the
  //    box's cp-subscribe endpoint. This lets the always-on CP send a generic
  //    content-blind "new mail" notice — signed with the CP's OWN key — while the
  //    cell sleeps. No cell key material is ever involved. Best-effort: if the CP
  //    is not configured (self-host) or anything fails, the cell-keyed direct path
  //    above still works, so we swallow errors and return the primary subscription.
  await enableCPPush(deps, registrationOf(subscription)).catch(() => {})
  return subscription
}

// registrationOf recovers the ServiceWorkerRegistration for a subscription so the
// CP-keyed subscribe can reuse the same PushManager. Falls back to ready.
async function registrationOf(_subscription: PushSubscription): Promise<ServiceWorkerRegistration | null> { // eslint-disable-line no-unused-vars -- kept for call-site clarity; no browser API derives a registration from a subscription
  try {
    return await navigator.serviceWorker.ready
  } catch {
    return null
  }
}

// enableCPPush subscribes a SECOND time with the CP's PUBLIC VAPID key and
// forwards that CP-keyed subscription to the box (POST cp-subscribe). It is a
// best-effort no-op when the box has no CP configured (cp-key enabled:false) or
// the browser cannot create the second subscription. Never throws to the caller.
export async function enableCPPush(deps: WebPushDeps = {}, registrationPromise?: RegistrationSource): Promise<boolean> {
  const doFetch = deps.fetch || (typeof fetch !== 'undefined' ? fetch : null)
  if (!doFetch || !pushSupported()) return false

  // a) Ask the box (which relays from the CP) for the CP's PUBLIC VAPID key.
  let cpKey: unknown
  try {
    const res = await doFetch('/api/mail/push/cp-key', { headers: { Accept: 'application/json' } })
    if (!res || !res.ok) return false
    cpKey = await res.json()
  } catch {
    return false
  }
  if (!isRecord(cpKey) || !cpKey.enabled || typeof cpKey.vapid_public !== 'string') return false // self-host / no CP

  // b) Subscribe with the CP key on a SEPARATE service-worker scope so it does not
  //    clash with the cell-keyed subscription (a single PushManager holds at most
  //    one subscription, keyed by applicationServerKey; subscribing a second,
  //    different key on the SAME registration throws InvalidStateError). Callers
  //    that register a dedicated CP scope pass it via deps.cpRegistrationPromise.
  //    When no separate scope is available we still attempt on the primary
  //    registration and degrade gracefully — the wake-then-cell-pushes path covers
  //    delivery either way, so send-on-behalf is a best-effort optimisation.
  let cpSub: PushSubscription | null | undefined
  try {
    const reg =
      (deps.cpRegistrationPromise || registrationPromise || navigator.serviceWorker.ready)
    const registration = await reg
    if (!registration) return false
    // Reuse an existing subscription only if it already matches the CP key; a
    // mismatched existing key means we cannot add a second one here.
    const existing = await registration.pushManager.getSubscription()
    if (existing && deps.cpRegistrationPromise) {
      cpSub = existing
    } else if (existing && !deps.cpRegistrationPromise) {
      // Primary scope already holds the cell-keyed subscription; do not clobber it.
      return false
    } else {
      cpSub = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(cpKey.vapid_public),
      })
    }
  } catch {
    // A pre-existing subscription with a different key throws InvalidStateError.
    // Best-effort: skip the CP-keyed path; the direct path already works.
    return false
  }
  if (!cpSub) return false

  // c) Forward the CP-keyed subscription to the box, which relays it to the CP
  //    registrar. NO VAPID key material crosses — only endpoint + keys.
  try {
    const body: unknown = cpSub.toJSON ? cpSub.toJSON() : cpSub
    const endpoint = isRecord(body) && typeof body.endpoint === 'string' ? body.endpoint : undefined
    const keys = isRecord(body) && isRecord(body.keys) ? body.keys : undefined
    const res = await doFetch('/api/notifications/push/cp-subscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint, keys }),
    })
    return !!(res && res.ok)
  } catch {
    return false
  }
}

// disableWebPush unsubscribes this device and tells the box to forget it. Best
// effort; returns true when the box acknowledged the removal.
export async function disableWebPush(deps: WebPushDeps = {}): Promise<boolean> {
  const doFetch = deps.fetch || (typeof fetch !== 'undefined' ? fetch : null)
  if (!doFetch || !pushSupported()) return false
  try {
    const reg = deps.registrationPromise || navigator.serviceWorker.ready
    const registration = await reg
    // A null/undefined registration here would have thrown on the next line
    // and been caught by the outer catch (returning false) in the original
    // untyped code; this check makes that identical outcome explicit for the
    // type checker rather than relying on the throw.
    if (!registration) return false
    const subscription = await registration.pushManager.getSubscription()
    if (!subscription) return true // already gone
    const endpoint = subscription.endpoint
    await subscription.unsubscribe().catch(() => {})
    const res = await doFetch('/api/notifications/push/subscribe', {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint }),
    })
    // Also tell the box to forget the CP-keyed subscription (managed mode).
    // Best-effort — the endpoint is the same one when a single scope was used.
    await doFetch('/api/notifications/push/cp-subscribe', {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint }),
    }).catch(() => {})
    return !!(res && res.ok)
  } catch {
    return false
  }
}
