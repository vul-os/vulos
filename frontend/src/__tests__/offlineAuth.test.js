import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import { wrapMasterKeyWithPassword, getMasterKey, clearMasterKey } from '../lib/masterKey.js'
import * as oa from '../lib/offlineAuth.js'

// In-memory KV so the crypto/attempt/wipe logic runs under jsdom (no IndexedDB).
function memStore() {
  const m = new Map()
  return {
    m,
    get: (k) => Promise.resolve(m.has(k) ? m.get(k) : null),
    set: (k, v) => { m.set(k, v); return Promise.resolve() },
    del: (k) => { m.delete(k); return Promise.resolve() },
  }
}

const PASSWORD = 'correct horse battery staple'
let mk

async function enroll() {
  mk = crypto.getRandomValues(new Uint8Array(32))
  // Use the iteration FLOOR (not the 600k default) for test envelopes: exactly at
  // MIN_PBKDF2_ITERS (so cacheEnvelope still accepts it), but ~6x faster, which
  // matters for the wipe test that does MAX consecutive unwraps sequentially.
  const slot = await wrapMasterKeyWithPassword(mk, PASSWORD, 100000)
  await oa.cacheEnvelope(slot)
  await oa.cacheIdentity({ id: 'u1', name: 'Ada' })
}

beforeEach(() => {
  oa.__setStoreForTests(memStore())
  clearMasterKey()
})
afterEach(() => { clearMasterKey() })

describe('offlineAuth — enrollment + gate', () => {
  it('is not enrolled and cannot unlock before an online login', async () => {
    expect(await oa.isEnrolled()).toBe(false)
    await expect(oa.unlockOffline(PASSWORD)).rejects.toMatchObject({ code: 'NOT_ENROLLED' })
    expect(oa.isUnlocked()).toBe(false)
  })

  it('unlocks with the correct password and holds the master key in memory', async () => {
    await enroll()
    expect(await oa.isEnrolled()).toBe(true)
    expect(oa.isUnlocked()).toBe(false)

    await expect(oa.unlockOffline(PASSWORD)).resolves.toBe(true)
    expect(oa.isUnlocked()).toBe(true)
    expect(getMasterKey()).toEqual(mk)
  })

  it('records the enrolled identity for the lock screen', async () => {
    await enroll()
    expect(await oa.getCachedIdentity()).toEqual({ id: 'u1', name: 'Ada' })
  })

  it('lock() drops the key; unlocking again is required', async () => {
    await enroll()
    await oa.unlockOffline(PASSWORD)
    oa.lock()
    expect(oa.isUnlocked()).toBe(false)
    expect(getMasterKey()).toBe(null)
  })
})

describe('offlineAuth — fail-closed on wrong password', () => {
  it('a wrong password yields NOTHING and never unlocks (GCM tag fails)', async () => {
    await enroll()
    await expect(oa.unlockOffline('nope')).rejects.toMatchObject({ code: 'WRONG_PASSWORD' })
    expect(oa.isUnlocked()).toBe(false)
    expect(getMasterKey()).toBe(null)
  })

  it('reports decreasing attempts-left and still unlocks with the right password', async () => {
    await enroll()
    const e1 = await oa.unlockOffline('x').catch(e => e)
    expect(e1.attemptsLeft).toBe(oa.MAX_OFFLINE_ATTEMPTS - 1)
    // The correct password still works and resets the counter.
    await expect(oa.unlockOffline(PASSWORD)).resolves.toBe(true)
    oa.lock()
    const e2 = await oa.unlockOffline('x').catch(e => e)
    expect(e2.attemptsLeft).toBe(oa.MAX_OFFLINE_ATTEMPTS - 1) // counter was reset by the success
  })

  it('wipes the cached envelope after MAX consecutive wrong attempts', async () => {
    await enroll()
    const wipeSpy = vi.fn()
    window.addEventListener('vulos:offline-wipe', wipeSpy)

    let lastErr
    for (let i = 0; i < oa.MAX_OFFLINE_ATTEMPTS; i++) {
      lastErr = await oa.unlockOffline('wrong').catch(e => e)
    }
    expect(lastErr.code).toBe('WIPED')
    expect(await oa.isEnrolled()).toBe(false) // envelope gone
    expect(wipeSpy).toHaveBeenCalled()        // apps signalled to drop caches
    // And no further offline unlock is possible, even with the right password.
    await expect(oa.unlockOffline(PASSWORD)).rejects.toMatchObject({ code: 'NOT_ENROLLED' })

    window.removeEventListener('vulos:offline-wipe', wipeSpy)
  })
})

describe('offlineAuth — envelope validation (anti-downgrade)', () => {
  it('refuses to cache a weak/downgraded envelope (iter below the floor)', async () => {
    await expect(
      oa.cacheEnvelope({ kdf: 'pbkdf2-sha256', iter: 1, salt: 'a', iv: 'b', ct: 'c' }),
    ).rejects.toThrow(/invalid or weak/i)
    expect(await oa.isEnrolled()).toBe(false)
  })

  it('treats a structurally-invalid stored envelope as CORRUPT (not a wrong-password attempt) and wipes it', async () => {
    const store = memStore()
    oa.__setStoreForTests(store)
    // Inject a low-iter envelope directly, bypassing cacheEnvelope's guard.
    await store.set('envelope', { kdf: 'pbkdf2-sha256', iter: 1, salt: 'a', iv: 'b', ct: 'c' })

    const err = await oa.unlockOffline('anything').catch(e => e)
    expect(err.code).toBe('CORRUPT')
    // Wiped, and no wrong-password attempt was recorded (it was structural).
    expect(await oa.isEnrolled()).toBe(false)
    expect(await store.get('attempts')).toBe(null)
  })
})

describe('offlineAuth — per-app key isolation', () => {
  it('derives distinct, non-extractable keys per app; different apps cannot read each other', async () => {
    await enroll()
    await oa.unlockOffline(PASSWORD)

    const notesKey = await oa.appKey('notes')
    const mailKey = await oa.appKey('mail')
    expect(notesKey.extractable).toBe(false)

    // Same plaintext + iv under each app key must produce DIFFERENT ciphertext,
    // proving the keys differ (HKDF per app) — i.e. per-app isolation.
    const iv = new Uint8Array(12)
    const pt = new TextEncoder().encode('secret note')
    const cNotes = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, notesKey, pt))
    const cMail = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, mailKey, pt))
    expect(Array.from(cNotes)).not.toEqual(Array.from(cMail))

    // The SAME app derives the SAME key (stable) — notes can decrypt its own data.
    const notesKey2 = await oa.appKey('notes')
    const cNotes2 = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, notesKey2, pt))
    expect(Array.from(cNotes2)).toEqual(Array.from(cNotes))
  })

  it('appKey fails closed when locked', async () => {
    await enroll()
    await expect(oa.appKey('notes')).rejects.toMatchObject({ code: 'LOCKED' })
  })
})

describe('offlineAuth — wipe', () => {
  it('removes the credential and signals apps, but preserves the live session key', async () => {
    await enroll()
    await oa.unlockOffline(PASSWORD)
    const wipeSpy = vi.fn()
    window.addEventListener('vulos:offline-wipe', wipeSpy)

    await oa.wipe()

    // Credential gone; apps signalled.
    expect(await oa.isEnrolled()).toBe(false)
    expect(await oa.getCachedIdentity()).toBe(null)
    expect(wipeSpy).toHaveBeenCalled()
    // But the live session key is NOT cleared — "Forget offline data" runs while
    // online-authenticated and must not break the current session's content crypto.
    expect(oa.isUnlocked()).toBe(true)
    expect(getMasterKey()).toEqual(mk)

    window.removeEventListener('vulos:offline-wipe', wipeSpy)
  })
})
