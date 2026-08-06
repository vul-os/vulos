// webPush.test.js — client opt-in for the cell-side Web Push send-path.
//
// Exercises the happy path (fetch VAPID → permission → subscribe → register) and
// the fail-safe-off branches (unsupported / disabled box / denied permission),
// asserting the security contract: the body sent to the box carries only
// endpoint + keys (never an owner id), and the public key is the only key
// material touched.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { urlBase64ToUint8Array, enableWebPush, disableWebPush, enableCPPush, type WebPushDeps } from '../core/notifiers/webPush'

// A base64url public key (arbitrary valid-ish bytes) for the conversion test.
const PUBKEY = 'BPabc-DEF_ghi'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// jsdom's native atob is base64-spec-strict and throws on this test's
// deliberately loose "arbitrary valid-ish bytes" PUBKEY fixture (after
// urlBase64ToUint8Array's own padding math it ends up with 3 trailing '='
// characters, which isn't valid base64). Node's Buffer.from(…, 'base64') is
// lenient enough to decode it anyway; since @types/node isn't installed here,
// this reimplements just that leniency (skip anything outside the base64
// alphabet, decode whatever's left) instead of pulling in a Buffer type this
// project doesn't otherwise use. Verified byte-for-byte identical to the
// original Buffer-based shim's output for this fixture.
function lenientAtob(input: string): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
  const clean = input.split('').filter((c) => alphabet.includes(c)).join('')
  let bits = 0
  let value = 0
  let out = ''
  for (const c of clean) {
    value = (value << 6) | alphabet.indexOf(c)
    bits += 6
    if (bits >= 8) {
      bits -= 8
      out += String.fromCharCode((value >> bits) & 0xff)
    }
  }
  return out
}

function setNotification(permission: NotificationPermission, requestResult: NotificationPermission): void {
  Object.defineProperty(globalThis, 'Notification', {
    value: { permission, requestPermission: async () => requestResult },
    configurable: true,
  })
}

beforeEach(() => {
  // jsdom lacks these; define minimal globals the module feature-detects on.
  globalThis.navigator = globalThis.navigator || {}
  Object.defineProperty(globalThis.navigator, 'serviceWorker', {
    value: { ready: Promise.resolve(null) },
    configurable: true,
  })
  Object.defineProperty(globalThis, 'PushManager', { value: function () {}, configurable: true })
  setNotification('granted', 'granted')
  globalThis.atob = (s) => lenientAtob(s)
})

interface FakeSubscription {
  endpoint: string
  toJSON: () => { endpoint: string; keys: { p256dh: string; auth: string } }
  unsubscribe: () => Promise<boolean>
}

function fakeSubscription(endpoint = 'https://push.example/dev1'): FakeSubscription {
  return {
    endpoint,
    toJSON: () => ({ endpoint, keys: { p256dh: 'PK', auth: 'AK' } }),
    unsubscribe: async () => true,
  }
}

interface FakeRegistration {
  pushManager: {
    getSubscription: () => Promise<FakeSubscription | null>
    subscribe: () => Promise<FakeSubscription>
  }
}

function fakeRegistration(existing: FakeSubscription | null = null, subscribeResult: FakeSubscription = fakeSubscription()): Promise<FakeRegistration> {
  return Promise.resolve({
    pushManager: {
      getSubscription: async () => existing,
      subscribe: async () => subscribeResult,
    },
  })
}

// ServiceWorkerRegistration is a large DOM interface (EventTarget + a
// CookieStoreManager + a full PushManager, …) that jsdom does not implement
// and this test has no honest way to construct — webPush.ts only ever reads
// registration.pushManager.{getSubscription,subscribe} (see its deps
// comment: "registrationPromise is injectable for tests"). Object.defineProperty's
// `value` is untyped — same escape hatch as the navigator.serviceWorker stub
// above — so it can carry this duck-typed stand-in without a project cast.
function withRegistration(
  deps: WebPushDeps,
  key: 'registrationPromise' | 'cpRegistrationPromise',
  reg: Promise<FakeRegistration | null>,
): WebPushDeps {
  Object.defineProperty(deps, key, { value: reg, enumerable: true, configurable: true })
  return deps
}

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), { status: 200, ...init })
}

function parseJsonBody(init: RequestInit | undefined): Record<string, unknown> {
  if (!init || typeof init.body !== 'string') throw new Error('expected a JSON string body')
  const parsed: unknown = JSON.parse(init.body)
  if (!isRecord(parsed)) throw new Error('expected a JSON object body')
  return parsed
}

interface Call {
  url: string
  init?: RequestInit
}

describe('urlBase64ToUint8Array', () => {
  it('decodes a base64url string into bytes', () => {
    const out = urlBase64ToUint8Array(PUBKEY)
    expect(out).toBeInstanceOf(Uint8Array)
    expect(out.length).toBeGreaterThan(0)
  })
})

describe('enableWebPush', () => {
  it('subscribes and registers with the box, sending only endpoint+keys', async () => {
    const calls: Call[] = []
    const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const u = String(url)
      calls.push({ url: u, init })
      if (u.endsWith('/vapid-public')) {
        return jsonResponse({ enabled: true, publicKey: PUBKEY })
      }
      return jsonResponse({ status: 'subscribed' })
    })
    const deps = withRegistration(
      { fetch, requestPermission: async (): Promise<NotificationPermission> => 'granted' },
      'registrationPromise',
      fakeRegistration(),
    )
    const sub = await enableWebPush(deps)
    expect(sub).toBeTruthy()
    const post = calls.find((c) => c.init && c.init.method === 'POST')
    expect(post).toBeTruthy()
    if (!post) throw new Error('expected a POST call')
    const body = parseJsonBody(post.init)
    // Security contract: no owner/account id in the body; only endpoint + keys.
    expect(Object.keys(body).sort()).toEqual(['endpoint', 'keys'])
    expect(body.endpoint).toBe('https://push.example/dev1')
    expect(body).not.toHaveProperty('owner_id')
    expect(body).not.toHaveProperty('account_id')
  })

  it('is fail-safe-off when the box has push disabled', async () => {
    const fetch = vi.fn(async (): Promise<Response> => jsonResponse({ enabled: false }))
    const deps = withRegistration({ fetch }, 'registrationPromise', fakeRegistration())
    const sub = await enableWebPush(deps)
    expect(sub).toBeNull()
    // Never attempts to POST a subscription when the box reports disabled.
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('returns null when permission is denied', async () => {
    setNotification('default', 'denied')
    const fetch = vi.fn(async (url: RequestInfo | URL): Promise<Response> =>
      String(url).endsWith('/vapid-public')
        ? jsonResponse({ enabled: true, publicKey: PUBKEY })
        : jsonResponse({})
    )
    const deps = withRegistration(
      { fetch, requestPermission: async (): Promise<NotificationPermission> => 'denied' },
      'registrationPromise',
      fakeRegistration(),
    )
    const sub = await enableWebPush(deps)
    expect(sub).toBeNull()
  })

  it('reuses an existing subscription rather than re-subscribing', async () => {
    const existing = fakeSubscription('https://push.example/existing')
    const fetch = vi.fn(async (url: RequestInfo | URL): Promise<Response> =>
      String(url).endsWith('/vapid-public')
        ? jsonResponse({ enabled: true, publicKey: PUBKEY })
        : jsonResponse({})
    )
    const deps = withRegistration({ fetch }, 'registrationPromise', fakeRegistration(existing))
    const sub = await enableWebPush(deps)
    expect(sub).toBe(existing)
  })
})

describe('enableCPPush (send-on-behalf, CP-keyed)', () => {
  it('forwards a CP-keyed subscription carrying only endpoint+keys, NO VAPID material', async () => {
    const calls: Call[] = []
    const cpSub = fakeSubscription('https://push.example/cp-keyed')
    const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const u = String(url)
      calls.push({ url: u, init })
      if (u.endsWith('/cp-key')) {
        return jsonResponse({ enabled: true, vapid_public: PUBKEY, subject: 'mailto:cp@example.com' })
      }
      return jsonResponse({ status: 'cp-subscribed' })
    })
    // Provide a dedicated CP scope with the CP-keyed subscription pre-created.
    const deps = withRegistration({ fetch }, 'cpRegistrationPromise', fakeRegistration(cpSub))
    const ok = await enableCPPush(deps)
    expect(ok).toBe(true)
    const post = calls.find((c) => c.url.endsWith('/cp-subscribe') && c.init && c.init.method === 'POST')
    expect(post).toBeTruthy()
    if (!post) throw new Error('expected a cp-subscribe POST call')
    const body = parseJsonBody(post.init)
    expect(Object.keys(body).sort()).toEqual(['endpoint', 'keys'])
    expect(body.endpoint).toBe('https://push.example/cp-keyed')
    // NO VAPID key material may cross the wire.
    expect(JSON.stringify(body).toLowerCase()).not.toContain('vapid')
    expect(body).not.toHaveProperty('vapid_private')
  })

  it('is inert (no cp-subscribe) when the box reports no CP configured (self-host)', async () => {
    const calls: string[] = []
    const fetch = vi.fn(async (url: RequestInfo | URL): Promise<Response> => {
      const u = String(url)
      calls.push(u)
      if (u.endsWith('/cp-key')) return jsonResponse({ enabled: false })
      return jsonResponse({})
    })
    const deps = withRegistration({ fetch }, 'cpRegistrationPromise', fakeRegistration())
    const ok = await enableCPPush(deps)
    expect(ok).toBe(false)
    expect(calls.some((u) => u.endsWith('/cp-subscribe'))).toBe(false)
  })
})

describe('disableWebPush', () => {
  it('unsubscribes and tells the box to forget the endpoint', async () => {
    const existing = fakeSubscription('https://push.example/gone')
    let deletedEndpoint: string | null = null
    const fetch = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if (init && init.method === 'DELETE' && typeof init.body === 'string') {
        const parsed: unknown = JSON.parse(init.body)
        if (isRecord(parsed) && typeof parsed.endpoint === 'string') deletedEndpoint = parsed.endpoint
      }
      return jsonResponse({})
    })
    const deps = withRegistration({ fetch }, 'registrationPromise', fakeRegistration(existing))
    const ok = await disableWebPush(deps)
    expect(ok).toBe(true)
    expect(deletedEndpoint).toBe('https://push.example/gone')
  })
})
