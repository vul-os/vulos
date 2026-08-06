import { describe, it, expect, afterEach } from 'vitest'
import * as Y from 'yjs'

import { persistYDoc, resolveOfflineKey, offlineHost, offlineStatus } from '../lib/offline/index.js'

// The on-disk record shape src/lib/offline/index.js writes/reads — that module
// stays untyped JS on purpose (see its header: zero Vulos imports, vendorable),
// so this mirrors its shape here rather than importing a type from it.
interface SnapshotRecord {
  enc: number
  iv?: Uint8Array
  ct?: Uint8Array
  u?: Uint8Array
}

// The window.vulos.offline gate persistYDoc/offlineHost look for (OFFLINE-AUTH-01).
// Not declared anywhere yet because the real implementation lives in the OS
// shell (out of scope here) and src/lib/offline/index.js itself is untyped JS.
interface OfflineHostGate {
  isUnlocked: () => Promise<boolean>
  appKey: () => Promise<CryptoKey>
}

declare global {
  interface Window {
    vulos?: { offline: OfflineHostGate }
  }
}

// In-memory KV so persistence runs under jsdom (no IndexedDB), same pattern the
// library uses for the real backend.
function memStore() {
  const m = new Map<string, SnapshotRecord>()
  return {
    m,
    get: (k: string) => Promise.resolve(m.has(k) ? m.get(k) : null),
    set: (k: string, v: SnapshotRecord) => { m.set(k, v); return Promise.resolve() },
    del: (k: string) => { m.delete(k); return Promise.resolve() },
  }
}

function readSnapshot(store: ReturnType<typeof memStore>): SnapshotRecord {
  const rec = store.m.get('snapshot')
  if (!rec) throw new Error('expected a persisted snapshot record')
  return rec
}

const genKey = () => crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt'])

describe('persistYDoc — offline-first local persistence', () => {
  it('restores a doc across sessions (plaintext)', async () => {
    const store = memStore()
    const d1 = new Y.Doc()
    const p1 = await persistYDoc(d1, { name: 't', store })
    await p1.whenSynced
    d1.getMap('m').set('hello', 'world')
    await p1.flush()
    p1.destroy()

    const d2 = new Y.Doc()
    const p2 = await persistYDoc(d2, { name: 't', store })
    await p2.whenSynced
    expect(d2.getMap('m').get('hello')).toBe('world')
    p2.destroy()
  })

  it('seals the cache at rest when a key is given, and restores it with that key', async () => {
    const store = memStore()
    const key = await genKey()
    const d1 = new Y.Doc()
    const p1 = await persistYDoc(d1, { name: 't', key, store })
    await p1.whenSynced
    d1.getMap('m').set('secret', 'value')
    await p1.flush()

    // On disk it is sealed — no plaintext update bytes.
    const rec = readSnapshot(store)
    expect(rec.enc).toBe(1)
    expect(rec.iv).toBeInstanceOf(Uint8Array)
    expect(rec.ct).toBeInstanceOf(Uint8Array)
    expect(rec.u).toBeUndefined()

    const d2 = new Y.Doc()
    const p2 = await persistYDoc(d2, { name: 't', key, store })
    await p2.whenSynced
    expect(d2.getMap('m').get('secret')).toBe('value')
    p2.destroy()
  })

  it('a wrong key cannot read the sealed cache — starts clean, never throws', async () => {
    const store = memStore()
    const key = await genKey()
    const d1 = new Y.Doc()
    const p1 = await persistYDoc(d1, { name: 't', key, store })
    await p1.whenSynced
    d1.getMap('m').set('secret', 'value')
    await p1.flush()

    const wrong = await genKey()
    const d2 = new Y.Doc()
    const p2 = await persistYDoc(d2, { name: 't', key: wrong, store })
    await expect(p2.whenSynced).resolves.toBe(true) // does not throw
    expect(d2.getMap('m').get('secret')).toBeUndefined()
    p2.destroy()
  })

  it('clear() erases the local cache', async () => {
    const store = memStore()
    const d1 = new Y.Doc()
    const p1 = await persistYDoc(d1, { name: 't', store })
    await p1.whenSynced
    d1.getMap('m').set('x', '1')
    await p1.flush()
    expect(store.m.has('snapshot')).toBe(true)
    await p1.clear()
    expect(store.m.has('snapshot')).toBe(false)
  })

  it('destroy() flushes the pending edit (no silent loss) and stops auto-persisting', async () => {
    const store = memStore()
    const d1 = new Y.Doc()
    const p1 = await persistYDoc(d1, { name: 't', store, debounceMs: 10_000 })
    await p1.whenSynced
    d1.getMap('m').set('pending', 'edit') // arms the debounce but won't fire for 10s
    await p1.destroy()                     // must flush the pending edit before teardown

    const d2 = new Y.Doc()
    const p2 = await persistYDoc(d2, { name: 't', store })
    await p2.whenSynced
    expect(d2.getMap('m').get('pending')).toBe('edit') // the pending edit was persisted
    p2.destroy()
  })

  it('refuses to overwrite a SEALED cache with plaintext when no key is available', async () => {
    const store = memStore()
    const key = await genKey()
    // Session 1: seal the cache with a key.
    const d1 = new Y.Doc()
    const p1 = await persistYDoc(d1, { name: 't', key, store })
    await p1.whenSynced
    d1.getMap('m').set('secret', 'value')
    await p1.flush()
    p1.destroy()
    expect(readSnapshot(store).enc).toBe(1)

    // Session 2: no key (host locked). The sealed cache can't be read (starts
    // clean) and — critically — an edit must NOT downgrade it to plaintext.
    const d2 = new Y.Doc()
    const p2 = await persistYDoc(d2, { name: 't', key: null, store })
    await p2.whenSynced
    d2.getMap('m').set('plaintext', 'leak')
    await p2.flush()
    // The on-disk record is STILL the sealed ciphertext — no downgrade, no leak.
    expect(readSnapshot(store).enc).toBe(1)
    expect(readSnapshot(store).u).toBeUndefined()
    p2.destroy()

    // Session 3: key back → the original sealed content is recoverable (not lost).
    const d3 = new Y.Doc()
    const p3 = await persistYDoc(d3, { name: 't', key, store })
    await p3.whenSynced
    expect(d3.getMap('m').get('secret')).toBe('value')
    p3.destroy()
  })
})

describe('offline host resolution — Vulos-optional, safe standalone', () => {
  afterEach(() => { try { delete window.vulos } catch { /* ignore */ } })

  it('has no host and no key standalone (plaintext / device-encryption)', async () => {
    expect(offlineHost()).toBe(null)
    expect(await resolveOfflineKey()).toBe(null)
    const st = await offlineStatus()
    expect(st).toEqual({ host: false, unlocked: false, encryptedAtRest: false })
  })

  it('uses an explicit override key regardless of host', async () => {
    const key = await genKey()
    expect(await resolveOfflineKey(key)).toBe(key)
  })

  it('sources key + unlock state from a Vulos host when present', async () => {
    const key = await genKey()
    window.vulos = { offline: { isUnlocked: async () => true, appKey: async () => key } }
    expect(offlineHost()).not.toBe(null)
    expect(await resolveOfflineKey()).toBe(key)
    expect(await offlineStatus()).toEqual({ host: true, unlocked: true, encryptedAtRest: true })
  })

  it('degrades safely when the host is locked (no key → plaintext, never throws)', async () => {
    window.vulos = { offline: { isUnlocked: async () => false, appKey: async () => { throw new Error('locked') } } }
    expect(await resolveOfflineKey()).toBe(null)
    const st = await offlineStatus()
    expect(st.host).toBe(true)
    expect(st.unlocked).toBe(false)
    expect(st.encryptedAtRest).toBe(false)
  })
})
