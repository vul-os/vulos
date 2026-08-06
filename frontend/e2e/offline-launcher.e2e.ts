// offline-launcher.e2e.js — the OS offline auth gate + a cached view, in a REAL
// browser (OFFLINE-AUTH-01).
//
// When the box is unreachable AND this device has a cached, password-wrapped
// master-key envelope (earned by a prior online login), the shell offers a
// fail-closed OFFLINE unlock instead of an online login that can't succeed
// (src/App.jsx AuthGate → OfflineLockScreen). Unlock unwraps the envelope LOCALLY
// with the account password via AES-GCM — a wrong password authenticates nothing
// (src/lib/offlineAuth.js). This spec drives that whole path against the shipping
// shell:
//
//   • we simulate "box unreachable" by aborting the reachability probe
//     (/api/auth/me) — exactly the fetch-throw AuthProvider treats as offline;
//   • we pre-seed IndexedDB with a REAL password envelope (built here with the
//     same PBKDF2-600k → AES-256-GCM wrap the client uses) + a cached identity,
//     so the device reads as offline-enrolled;
//   • the offline lock screen appears; the correct password unlocks to the shell
//     (the cached view); a wrong password fails closed and stays locked.
//
// The crypto/attempt/wipe internals are unit-tested under jsdom
// (src/__tests__/offlineAuth.test.js); this is the real-DOM, real-WebCrypto,
// real-IndexedDB proof of the end-to-end gate.

import { test, expect, type Page } from '@playwright/test'
import { webcrypto } from 'node:crypto'
import { installBackend } from './mock-backend.js'

const PASSWORD = 'correct horse battery staple'
const WRONG_PASSWORD = 'definitely not the password'
const IDENTITY = { id: 'u1', name: 'Ada Lovelace' }

// A 32-byte master key (offlineAuth rejects any unwrap that isn't exactly 32
// bytes as corrupt) — fixed for determinism.
const MASTER_KEY = new Uint8Array(32).fill(7)

const b64 = (u8: Uint8Array) => Buffer.from(u8).toString('base64')

interface PasswordEnvelope {
  v: number
  kdf: string
  iter: number
  salt: string
  iv: string
  ct: string
}

// buildPasswordEnvelope mirrors src/lib/masterKey.js wrapMasterKeyWithPassword:
// PBKDF2-HMAC-SHA256(password, salt, 600k) → AES-256-GCM(masterKey) with AAD
// 'vulos-mk-pw-v1'. The shell's unwrapMasterKeyWithPassword must accept it, so
// the shapes (kdf name, iter, base64 salt/iv/ct, AAD) match exactly.
async function buildPasswordEnvelope(
  masterKey: Uint8Array<ArrayBuffer>,
  password: string,
  iter = 600000,
): Promise<PasswordEnvelope> {
  const te = new TextEncoder()
  const salt = webcrypto.getRandomValues(new Uint8Array(16))
  const iv = webcrypto.getRandomValues(new Uint8Array(12))
  const baseKey = await webcrypto.subtle.importKey('raw', te.encode(password), 'PBKDF2', false, ['deriveKey'])
  const aesKey = await webcrypto.subtle.deriveKey(
    { name: 'PBKDF2', salt, iterations: iter, hash: 'SHA-256' },
    baseKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  )
  const ct = new Uint8Array(await webcrypto.subtle.encrypt(
    { name: 'AES-GCM', iv, additionalData: te.encode('vulos-mk-pw-v1') },
    aesKey,
    masterKey,
  ))
  return { v: 1, kdf: 'pbkdf2-sha256', iter, salt: b64(salt), iv: b64(iv), ct: b64(ct) }
}

let ENVELOPE: PasswordEnvelope

test.beforeAll(async () => {
  ENVELOPE = await buildPasswordEnvelope(MASTER_KEY, PASSWORD)
})

// Seed the offline-auth IndexedDB store the way the client's idbStore does
// (DB 'vulos-offline-auth' v1, out-of-line store 'kv'): the wrapped envelope at
// 'envelope' and the greeting identity at 'identity'.
async function seedOfflineEnrollment(
  page: Page,
  envelope: PasswordEnvelope,
  identity: typeof IDENTITY,
) {
  await page.evaluate(async ({ env, id }) => {
    await new Promise<void>((resolve, reject) => {
      const req = indexedDB.open('vulos-offline-auth', 1)
      req.onupgradeneeded = () => req.result.createObjectStore('kv')
      req.onerror = () => reject(req.error)
      req.onsuccess = () => {
        const db = req.result
        const t = db.transaction('kv', 'readwrite')
        const os = t.objectStore('kv')
        os.put(env, 'envelope')
        os.put({ id: id.id, name: id.name }, 'identity')
        t.oncomplete = () => { db.close(); resolve() }
        t.onerror = () => reject(t.error)
      }
    })
  }, { env: envelope, id: identity })
}

// Boot the shell with the box unreachable (auth/me aborts → AuthProvider marks
// `offline`) and the device offline-enrolled (seeded IDB). Returns after the
// offline lock screen has rendered.
async function bootOfflineLocked(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  await installBackend(page)
  // The reachability probe throws → the shell's definition of "offline" (box
  // unreachable), while static assets keep loading so the shell can run. Added
  // AFTER installBackend so this handler wins for /api/auth/me.
  await page.route('**/api/auth/me', (route) => route.abort())

  // First load: not yet enrolled → the shell shows online login. We only need the
  // page up so we can seed IndexedDB, then reload into the enrolled+offline state.
  await page.goto('/')
  await seedOfflineEnrollment(page, ENVELOPE, IDENTITY)
  await page.reload()

  // The offline lock screen — unique honest copy, not the online LockScreen.
  await expect(page.getByText(/unlock to view your cached data/i)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByLabel('Account password')).toBeVisible()
  return errors
}

// A shell chrome marker present in every layout (desktop + mobile): the menubar
// notification bell. Its presence proves the shell mounted (the cached view).
const shellMounted = (page: Page) =>
  page.getByRole('button', { name: /unread|^Notifications$/i }).first()

test('the offline lock screen appears when the box is unreachable and the device is enrolled', async ({ page }) => {
  const errors = await bootOfflineLocked(page)

  await expect(page.getByText('Ada Lovelace')).toBeVisible() // greeted by cached identity
  await expect(page.getByRole('button', { name: 'Unlock' })).toBeVisible()
  // Fail-closed until unlocked: the shell has NOT mounted.
  await expect(shellMounted(page)).toHaveCount(0)

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the correct password unlocks locally and the cached view renders', async ({ page }) => {
  await bootOfflineLocked(page)

  await page.getByLabel('Account password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Unlock' }).click()

  // AES-GCM unwrap succeeds → offline session starts → the shell (cached view)
  // mounts and the lock screen unmounts.
  await expect(shellMounted(page)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText(/unlock to view your cached data/i)).toBeHidden()
})

test('a wrong password fails closed — the shell never mounts', async ({ page }) => {
  await bootOfflineLocked(page)

  await page.getByLabel('Account password').fill(WRONG_PASSWORD)
  await page.getByRole('button', { name: 'Unlock' }).click()

  // The GCM tag fails → the lock screen surfaces the incorrect-password error and
  // stays put; no offline session, no shell.
  await expect(page.getByRole('alert')).toContainText(/incorrect password/i, { timeout: 10_000 })
  await expect(page.getByText(/unlock to view your cached data/i)).toBeVisible()
  await expect(shellMounted(page)).toHaveCount(0)
})
