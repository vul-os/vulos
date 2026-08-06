import { describe, it, expect, afterEach } from 'vitest'

import { hasSubtleCrypto, requireSubtle, INSECURE_CONTEXT_MESSAGE, type TaggedError } from '../lib/secureContext.js'
import { deriveContentKey } from '../lib/masterKey.js'
import { isSealed } from '../lib/contentSeal.js'

function isTaggedError(e: unknown): e is TaggedError {
  return e instanceof Error && 'code' in e && typeof e.code === 'string'
}

// WHY THIS TEST EXISTS
//
// `crypto.subtle` is undefined outside a browser "secure context", and the
// shipped box is reached over plain http:// on a LAN IP or a .local name —
// neither of which qualifies. Before the guard, every crypto entry point in
// src/lib died with a raw "Cannot read properties of undefined (reading
// 'importKey')" TypeError that told the user nothing.
//
// A guard that cannot be observed to FIRE is worth nothing, so this test
// asserts BOTH directions: the normal path is unchanged, and the insecure
// path throws our tagged error rather than a TypeError. The insecure case is
// the one that matters — it is the case that actually ships.

const realCrypto = globalThis.crypto

/** Simulate an insecure context by removing SubtleCrypto. */
function withoutSubtle<T>(fn: () => T): T {
  Object.defineProperty(globalThis, 'crypto', {
    value: { getRandomValues: realCrypto.getRandomValues.bind(realCrypto) },
    configurable: true,
    writable: true,
  })
  return fn()
}

afterEach(() => {
  Object.defineProperty(globalThis, 'crypto', {
    value: realCrypto,
    configurable: true,
    writable: true,
  })
})

describe('secureContext guard', () => {
  it('reports availability in a secure context (control)', () => {
    expect(hasSubtleCrypto()).toBe(true)
    expect(requireSubtle()).toBe(realCrypto.subtle)
  })

  it('reports unavailability when SubtleCrypto is missing', () => {
    withoutSubtle(() => {
      expect(hasSubtleCrypto()).toBe(false)
    })
  })

  it('throws a tagged, explanatory error instead of a TypeError', () => {
    withoutSubtle(() => {
      let err: unknown
      try {
        requireSubtle()
      } catch (e) {
        err = e
      }
      expect(err).toBeDefined()
      // The whole point: NOT a TypeError from dereferencing undefined.
      expect(err).not.toBeInstanceOf(TypeError)
      if (!isTaggedError(err)) throw new Error('expected requireSubtle() to throw a TaggedError')
      expect(err.code).toBe('INSECURE_CONTEXT')
      expect(err.message).toBe(INSECURE_CONTEXT_MESSAGE)
      // The message must actually tell the user how to fix it.
      expect(err.message).toMatch(/https:\/\//)
      expect(err.message).toMatch(/localhost/)
    })
  })
})

describe('src/lib crypto entry points are guarded', () => {
  it('masterKey.deriveContentKey works normally in a secure context (control)', async () => {
    const mk = new Uint8Array(32).fill(7)
    const out = await deriveContentKey(mk, 'vulos-test', 'id-1')
    expect(out).toBeInstanceOf(Uint8Array)
    expect(out.length).toBeGreaterThan(0)
  })

  it('masterKey.deriveContentKey surfaces the guard, not a TypeError', async () => {
    const mk = new Uint8Array(32).fill(7)
    await withoutSubtle(async () => {
      await expect(deriveContentKey(mk, 'vulos-test', 'id-1')).rejects.toMatchObject({
        code: 'INSECURE_CONTEXT',
      })
    })
  })

  it('non-crypto exports still work in an insecure context', () => {
    // isSealed() only inspects bytes, so it must NOT be gated — guarding it
    // would break callers that merely ask "is this blob sealed?" on a page
    // that never touches crypto.
    withoutSubtle(() => {
      expect(() => isSealed(new Uint8Array([1, 2, 3]))).not.toThrow()
    })
  })
})
