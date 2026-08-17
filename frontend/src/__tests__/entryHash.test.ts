/**
 * entryHash.test.ts — the provenance guard's hash predicate, both directions.
 *
 * `scripts/e2e-isolated.mjs` is how everything in this repository knows a test
 * result is about the bytes that run built, and this predicate is the part of it
 * that says "the served entry is content-addressed, so nobody else's build could
 * have produced that name". It had a false negative that rejected roughly a
 * fifth of all valid builds — deterministically, per tree, while reading as a
 * build failure rather than as a guard misfiring.
 *
 * A test that only asserted the fixed case would be satisfied by
 * `hasContentHash = () => true`, which is the hollow-guard shape this repository
 * keeps finding. So both directions are asserted, with a count over each corpus
 * so that emptying one cannot pass.
 */
import { describe, it, expect } from 'vitest'
import {
  hasContentHash, ENTRY_HASH_RE, HASH_ALPHABET, HASH_MIN_LENGTH,
} from '../../scripts/entryHash.mjs'

/**
 * Entries a real Vite build produces. The first is the one measured failing:
 * `C-4eASDL` is an ordinary 8-character base64URL digest whose second character
 * is a hyphen, and the old pattern matched from THAT hyphen, leaving six.
 */
const REAL_BUILDS = [
  '/assets/index-C-4eASDL.js',   // measured, rejected by the old pattern
  '/assets/index-DlLLTCyR.js',
  '/assets/index-8_sW8IZ0.js',
  '/assets/index-9RgV3ScH.js',
  '/assets/index-DsmUy5lQ.js',
  '/assets/index--bcdefgh.js',   // digest beginning with the hyphen
  '/assets/index-A-B-C-D1.js',   // several hyphens inside the digest
  '/assets/index-0123456789ab.js', // a longer digest than Vite's default 8
]

/**
 * Names that carry NO content hash. Every one of these is a name a DIFFERENT
 * build would also produce, which is precisely what makes the check worth
 * having — if any of these passed, the guard would be decorative.
 */
const NOT_HASHED = [
  '/assets/index.js',
  '/assets/main.js',
  '/assets/index-.js',
  '/assets/index-abc.js',        // seven short of the floor
  '/assets/index-1234567.js',    // one short of the floor
  '/assets/index-abcdefgh.css',  // right shape, wrong extension
  '/assets/index-abcd efgh.js',  // a space is not in the alphabet
  '/assets/index-abcd.efgh.js',  // a dot is not in the alphabet
]

describe('the provenance guard accepts every hash a real build produces', () => {
  // Coverage count: the corpus cannot be quietly emptied to make this vacuous.
  it('checks the whole corpus', () => {
    expect(REAL_BUILDS.length).toBe(8)
    expect(NOT_HASHED.length).toBe(8)
    // And the one that was actually observed failing is still in it.
    expect(REAL_BUILDS).toContain('/assets/index-C-4eASDL.js')
  })

  for (const entry of REAL_BUILDS) {
    it(`accepts ${entry}`, () => {
      expect(hasContentHash(entry), `${entry} is a content-addressed entry`).toBe(true)
    })
  }
})

describe('the provenance guard still rejects a name any build would produce', () => {
  for (const entry of NOT_HASHED) {
    it(`rejects ${entry}`, () => {
      expect(hasContentHash(entry), `${entry} carries no content hash`).toBe(false)
    })
  }
})

describe('the pattern is the one it claims to be', () => {
  it('uses the base64URL alphabet, hyphen included', () => {
    // Asserted on the alphabet itself and not only on outcomes: the hyphen is
    // the entire bug, and an outcome test would go green again the moment
    // someone widened the class to `.` for a different reason.
    expect(HASH_ALPHABET).toContain('-')
    expect(HASH_ALPHABET).toContain('_')
    expect(HASH_ALPHABET).toBe('A-Za-z0-9_-')
    // A wildcard would accept '/assets/index-abcd.efgh.js' above; it does not.
    expect(HASH_ALPHABET).not.toContain('.')
  })

  it('anchors at the end and requires at least Vite\'s digest length', () => {
    expect(HASH_MIN_LENGTH).toBe(8)
    expect(ENTRY_HASH_RE.source).toContain('$')
    // Not merely "contains a hash somewhere": a path segment that happens to
    // look hashed does not count if the FILE is not.
    expect(hasContentHash('/assets/index-abcdefgh/main.js')).toBe(false)
  })

  it('does not throw on a non-string', () => {
    // The caller reaches this after a failed regex match on index.html, so
    // `undefined` is a reachable input and must be a clean false.
    expect(hasContentHash(undefined)).toBe(false)
    expect(hasContentHash(null)).toBe(false)
  })
})
