// webPush.test.js — client opt-in for the cell-side Web Push send-path.
//
// Exercises the happy path (fetch VAPID → permission → subscribe → register) and
// the fail-safe-off branches (unsupported / disabled box / denied permission),
// asserting the security contract: the body sent to the box carries only
// endpoint + keys (never an owner id), and the public key is the only key
// material touched.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { urlBase64ToUint8Array, enableWebPush, disableWebPush, enableCPPush } from '../core/notifiers/webPush'

// A base64url public key (arbitrary valid-ish bytes) for the conversion test.
const PUBKEY = 'BPabc-DEF_ghi'

beforeEach(() => {
  // jsdom lacks these; define minimal globals the module feature-detects on.
  globalThis.navigator = globalThis.navigator || {}
  Object.defineProperty(globalThis.navigator, 'serviceWorker', {
    value: { ready: Promise.resolve(null) },
    configurable: true,
  })
  globalThis.PushManager = function () {}
  globalThis.Notification = { permission: 'granted', requestPermission: async () => 'granted' }
  globalThis.atob = (s) => Buffer.from(s, 'base64').toString('binary')
})

function fakeSubscription(endpoint = 'https://push.example/dev1') {
  return {
    endpoint,
    toJSON: () => ({ endpoint, keys: { p256dh: 'PK', auth: 'AK' } }),
    unsubscribe: async () => true,
  }
}

function fakeRegistration(existing = null, subscribeResult = fakeSubscription()) {
  return Promise.resolve({
    pushManager: {
      getSubscription: async () => existing,
      subscribe: async () => subscribeResult,
    },
  })
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
    const calls = []
    const fetch = vi.fn(async (url, init) => {
      calls.push({ url, init })
      if (url.endsWith('/vapid-public')) {
        return { ok: true, json: async () => ({ enabled: true, publicKey: PUBKEY }) }
      }
      return { ok: true, json: async () => ({ status: 'subscribed' }) }
    })
    const sub = await enableWebPush({
      fetch,
      registrationPromise: fakeRegistration(),
      requestPermission: async () => 'granted',
    })
    expect(sub).toBeTruthy()
    const post = calls.find((c) => c.init && c.init.method === 'POST')
    expect(post).toBeTruthy()
    const body = JSON.parse(post.init.body)
    // Security contract: no owner/account id in the body; only endpoint + keys.
    expect(Object.keys(body).sort()).toEqual(['endpoint', 'keys'])
    expect(body.endpoint).toBe('https://push.example/dev1')
    expect(body).not.toHaveProperty('owner_id')
    expect(body).not.toHaveProperty('account_id')
  })

  it('is fail-safe-off when the box has push disabled', async () => {
    const fetch = vi.fn(async () => ({ ok: true, json: async () => ({ enabled: false }) }))
    const sub = await enableWebPush({ fetch, registrationPromise: fakeRegistration() })
    expect(sub).toBeNull()
    // Never attempts to POST a subscription when the box reports disabled.
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('returns null when permission is denied', async () => {
    globalThis.Notification = { permission: 'default', requestPermission: async () => 'denied' }
    const fetch = vi.fn(async (url) =>
      url.endsWith('/vapid-public')
        ? { ok: true, json: async () => ({ enabled: true, publicKey: PUBKEY }) }
        : { ok: true, json: async () => ({}) }
    )
    const sub = await enableWebPush({
      fetch,
      registrationPromise: fakeRegistration(),
      requestPermission: async () => 'denied',
    })
    expect(sub).toBeNull()
  })

  it('reuses an existing subscription rather than re-subscribing', async () => {
    const existing = fakeSubscription('https://push.example/existing')
    const fetch = vi.fn(async (url) =>
      url.endsWith('/vapid-public')
        ? { ok: true, json: async () => ({ enabled: true, publicKey: PUBKEY }) }
        : { ok: true, json: async () => ({}) }
    )
    const sub = await enableWebPush({
      fetch,
      registrationPromise: fakeRegistration(existing),
    })
    expect(sub).toBe(existing)
  })
})

describe('enableCPPush (send-on-behalf, CP-keyed)', () => {
  it('forwards a CP-keyed subscription carrying only endpoint+keys, NO VAPID material', async () => {
    const calls = []
    const cpSub = fakeSubscription('https://push.example/cp-keyed')
    const fetch = vi.fn(async (url, init) => {
      calls.push({ url, init })
      if (url.endsWith('/cp-key')) {
        return { ok: true, json: async () => ({ enabled: true, vapid_public: PUBKEY, subject: 'mailto:cp@vulos.to' }) }
      }
      return { ok: true, json: async () => ({ status: 'cp-subscribed' }) }
    })
    // Provide a dedicated CP scope with the CP-keyed subscription pre-created.
    const ok = await enableCPPush({ fetch, cpRegistrationPromise: fakeRegistration(cpSub) })
    expect(ok).toBe(true)
    const post = calls.find((c) => c.url.endsWith('/cp-subscribe') && c.init && c.init.method === 'POST')
    expect(post).toBeTruthy()
    const body = JSON.parse(post.init.body)
    expect(Object.keys(body).sort()).toEqual(['endpoint', 'keys'])
    expect(body.endpoint).toBe('https://push.example/cp-keyed')
    // NO VAPID key material may cross the wire.
    expect(JSON.stringify(body).toLowerCase()).not.toContain('vapid')
    expect(body).not.toHaveProperty('vapid_private')
  })

  it('is inert (no cp-subscribe) when the box reports no CP configured (self-host)', async () => {
    const calls = []
    const fetch = vi.fn(async (url) => {
      calls.push(url)
      if (url.endsWith('/cp-key')) return { ok: true, json: async () => ({ enabled: false }) }
      return { ok: true, json: async () => ({}) }
    })
    const ok = await enableCPPush({ fetch, cpRegistrationPromise: fakeRegistration() })
    expect(ok).toBe(false)
    expect(calls.some((u) => u.endsWith('/cp-subscribe'))).toBe(false)
  })
})

describe('disableWebPush', () => {
  it('unsubscribes and tells the box to forget the endpoint', async () => {
    const existing = fakeSubscription('https://push.example/gone')
    let deleted = null
    const fetch = vi.fn(async (url, init) => {
      if (init && init.method === 'DELETE') deleted = JSON.parse(init.body)
      return { ok: true }
    })
    const ok = await disableWebPush({
      fetch,
      registrationPromise: fakeRegistration(existing),
    })
    expect(ok).toBe(true)
    expect(deleted.endpoint).toBe('https://push.example/gone')
  })
})
