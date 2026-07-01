import { describe, it, expect } from 'vitest'
import {
  unwrapMasterKeyWithPassword,
  unwrapMasterKeyWithPhrase,
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

function hex(u8) {
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
