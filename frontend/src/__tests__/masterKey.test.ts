import { describe, it, expect, vi } from 'vitest'

// These tests exercise real WebCrypto PBKDF2 at the production 600,000-iteration
// count (some cases derive twice). That is CPU-bound, and when vitest runs the
// full suite in parallel the derivations contend for cores — a single case can
// take >1.5s in isolation and considerably longer under load, occasionally
// tripping the default 5s per-test timeout (the noted masterKey/argon2 flake).
// Raise the per-test budget file-wide so the crypto stays honest (no weakened
// iteration count) and the suite is deterministic under parallel contention.
vi.setConfig({ testTimeout: 30000 })

import {
  unwrapMasterKeyWithPassword,
  unwrapMasterKeyWithPhrase,
  wrapMasterKeyWithPassword,
  resetPasswordWithActiveSession,
  deriveContentKey,
  holdMasterKey,
  getMasterKey,
  clearMasterKey,
} from '../lib/masterKey.js'

// Fixture produced by backend/internal/auth/masterkey.go (ProvisionMasterKey).
// Proves the JS WebCrypto unwrap is byte-for-byte wire-compatible with Go.
const FIXTURE = {
  env: {
    v: 1,
    pw: {
      kdf: 'pbkdf2-sha256',
      iter: 600000,
      salt: 'QdXXEweS8GOZdS1cDVybcA==',
      iv: 'fYwSPga6o2XfyKko',
      ct: 'OYRtIs30CnqXvns484/0ijy/hB0yr0DcZq99oEkWqC/qNvC0bDnWn9AjTDsooqxj',
    },
    phrase: {
      kdf: 'bip39-hkdf-sha256',
      iv: 'cNlMbNhxlBV82H+f',
      ct: 'ef33eo9GpqElEpuhaOjErbR54AWxdtvGw/A2EajeTriqQbRDHrJdWJnCBy+SpkKu',
    },
  },
  password: 'fixture-password-123',
  mnemonic:
    'maximum away copy luggage buffalo dilemma purse peanut crack stock elevator diagram rescue cram cherry catch omit return meat identify tunnel remove since fantasy',
  mkHex: '220a43647fd4f97d90c6d530594583a5dc8d12062c8bc89be61bd3bb811874a3',
}

function hex(u8: Uint8Array): string {
  return Array.from(u8).map((b) => b.toString(16).padStart(2, '0')).join('')
}

describe('masterKey client unwrap (Go wire parity)', () => {
  it('unwraps the master key from the password slot', async () => {
    const slot = { ...FIXTURE.env.pw, v: FIXTURE.env.v }
    const mk = await unwrapMasterKeyWithPassword(slot, FIXTURE.password)
    expect(mk).toBeInstanceOf(Uint8Array)
    expect(mk.length).toBe(32)
    expect(hex(mk)).toBe(FIXTURE.mkHex)
  })

  it('unwraps the SAME master key from the recovery phrase', async () => {
    const mk = await unwrapMasterKeyWithPhrase(FIXTURE.env.phrase, FIXTURE.mnemonic)
    expect(hex(mk)).toBe(FIXTURE.mkHex)
  })

  it('normalises the phrase (case/whitespace) before deriving', async () => {
    const messy = '  ' + FIXTURE.mnemonic.toUpperCase().replace(/ /g, '   ') + '  '
    const mk = await unwrapMasterKeyWithPhrase(FIXTURE.env.phrase, messy)
    expect(hex(mk)).toBe(FIXTURE.mkHex)
  })

  it('fails closed on a wrong password', async () => {
    const slot = { ...FIXTURE.env.pw, v: FIXTURE.env.v }
    await expect(unwrapMasterKeyWithPassword(slot, 'wrong password')).rejects.toBeTruthy()
  })

  it('fails closed on tampered ciphertext', async () => {
    const tampered = { ...FIXTURE.env.pw, v: FIXTURE.env.v, ct: 'AAAA' + FIXTURE.env.pw.ct.slice(4) }
    await expect(unwrapMasterKeyWithPassword(tampered, FIXTURE.password)).rejects.toBeTruthy()
  })

  it('rejects unsupported kdf', async () => {
    await expect(
      unwrapMasterKeyWithPassword({ kdf: 'argon2id', iter: 1, salt: '', iv: '', ct: '' }, 'x'),
    ).rejects.toThrow(/unsupported/)
  })
})

describe('wrapMasterKeyWithPassword (Tier-2 re-wrap, Go wire parity)', () => {
  it('round-trips: a JS-wrapped slot unwraps to the same master key', async () => {
    const mk = await unwrapMasterKeyWithPassword({ ...FIXTURE.env.pw, v: 1 }, FIXTURE.password)
    const slot = await wrapMasterKeyWithPassword(mk, 'a brand new password 123')
    // Wire shape matches the Go password slot.
    expect(slot.kdf).toBe('pbkdf2-sha256')
    expect(slot.iter).toBe(600000)
    expect(typeof slot.salt).toBe('string')
    expect(typeof slot.iv).toBe('string')
    expect(typeof slot.ct).toBe('string')
    // The new-password slot unwraps to the SAME key.
    const mk2 = await unwrapMasterKeyWithPassword(slot, 'a brand new password 123')
    expect(hex(mk2)).toBe(FIXTURE.mkHex)
    // The old password no longer opens the new slot.
    await expect(unwrapMasterKeyWithPassword(slot, FIXTURE.password)).rejects.toBeTruthy()
  })

  it('uses a fresh random salt/iv each call', async () => {
    const mk = new Uint8Array(32).fill(3)
    const a = await wrapMasterKeyWithPassword(mk, 'pw')
    const b = await wrapMasterKeyWithPassword(mk, 'pw')
    expect(a.salt).not.toBe(b.salt)
    expect(a.iv).not.toBe(b.iv)
    expect(a.ct).not.toBe(b.ct)
  })
})

// Local shape of the RequestInit the module actually passes to fetchImpl
// (always a JSON-stringified body) — avoids fighting the DOM `RequestInit`/
// `Response` types for a stand-in that only needs to mimic what the caller
// reads back (`res.ok`, `res.status`, `res.json()`).
interface FakeFetchOpts {
  method?: string
  headers?: Record<string, string>
  body: string
}
interface FakeFetchResponse {
  ok: boolean
  status: number
  json: () => Promise<Record<string, unknown>>
}
interface PostedResetBody {
  new_password: string
  password_slot: unknown
}

describe('resetPasswordWithActiveSession', () => {
  it('posts a client-wrapped slot when the key is held in memory', async () => {
    const mk = await unwrapMasterKeyWithPassword({ ...FIXTURE.env.pw, v: 1 }, FIXTURE.password)
    holdMasterKey(mk)
    let captured!: { url: string; body: PostedResetBody }
    // resetPasswordWithActiveSession's fetchImpl parameter defaults to the
    // global `fetch` (typed `typeof fetch`, i.e. it must return a real
    // `Response`); stubbing the global lets this stand-in return a partial
    // response shape instead of satisfying that full DOM interface.
    vi.stubGlobal('fetch', async (url: string, opts: FakeFetchOpts): Promise<FakeFetchResponse> => {
      captured = { url, body: JSON.parse(opts.body) }
      return { ok: true, status: 200, json: async () => ({ status: 'password reset' }) }
    })
    const out = await resetPasswordWithActiveSession('a fresh new pass!!')
    expect(out.ok).toBe(true)
    expect(captured.url).toBe('/api/auth/masterkey/reset-active')
    expect(captured.body.new_password).toBe('a fresh new pass!!')
    // The posted slot re-wraps the SAME key.
    const mk2 = await unwrapMasterKeyWithPassword(captured.body.password_slot, 'a fresh new pass!!')
    expect(hex(mk2)).toBe(FIXTURE.mkHex)
    clearMasterKey()
    vi.unstubAllGlobals()
  })

  it('fails to the phrase path (NO_MASTER_KEY) when no key is held', async () => {
    clearMasterKey()
    await expect(resetPasswordWithActiveSession('whatever new pw', async () => {
      throw new Error('fetch should not be called')
    })).rejects.toMatchObject({ code: 'NO_MASTER_KEY' })
  })

  it('surfaces NO_MASTER_KEY when the server returns 409', async () => {
    holdMasterKey(new Uint8Array(32).fill(9))
    vi.stubGlobal('fetch', async (): Promise<FakeFetchResponse> => (
      { ok: false, status: 409, json: async () => ({ error: 'no master key' }) }
    ))
    await expect(resetPasswordWithActiveSession('another new pw!!'))
      .rejects.toMatchObject({ code: 'NO_MASTER_KEY' })
    clearMasterKey()
    vi.unstubAllGlobals()
  })
})

describe('deriveContentKey', () => {
  it('is deterministic and scoped by domain/id', async () => {
    const mk = await unwrapMasterKeyWithPassword({ ...FIXTURE.env.pw, v: 1 }, FIXTURE.password)
    const a1 = await deriveContentKey(mk, 'mail', 'msg-1')
    const a2 = await deriveContentKey(mk, 'mail', 'msg-1')
    const b = await deriveContentKey(mk, 'mail', 'msg-2')
    const c = await deriveContentKey(mk, 'files', 'msg-1')
    expect(hex(a1)).toBe(hex(a2))
    expect(hex(a1)).not.toBe(hex(b))
    expect(hex(a1)).not.toBe(hex(c))
    expect(a1.length).toBe(32)
  })

  it('domain/id boundary is injective', async () => {
    const mk = await unwrapMasterKeyWithPassword({ ...FIXTURE.env.pw, v: 1 }, FIXTURE.password)
    const x = await deriveContentKey(mk, 'a', 'bc')
    const y = await deriveContentKey(mk, 'ab', 'c')
    expect(hex(x)).not.toBe(hex(y))
  })
})

describe('in-memory holder', () => {
  it('holds and clears the key, never a non-32-byte key', () => {
    clearMasterKey()
    expect(getMasterKey()).toBeNull()
    const mk = new Uint8Array(32).fill(7)
    holdMasterKey(mk)
    expect(getMasterKey()).toBe(mk)
    clearMasterKey()
    expect(getMasterKey()).toBeNull()
    expect(mk.every((b) => b === 0)).toBe(true) // zeroed on clear
    expect(() => holdMasterKey(new Uint8Array(16))).toThrow()
  })
})
