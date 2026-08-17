/**
 * entryHash.mjs — "does this entry bundle name carry a content hash?"
 *
 * Extracted from e2e-isolated.mjs so it can be tested. That script calls
 * `main()` at module scope, so importing it to reach the pattern would start a
 * build; a leaf with no side effects can be asserted against directly, and this
 * particular predicate had a bug that no one could see.
 *
 * ── The bug, measured ──────────────────────────────────────────────────────
 *
 * The pattern was `-[A-Za-z0-9_]{8,}\.js$`. Vite names chunks with a base64URL
 * digest, and base64URL's alphabet is [A-Za-z0-9_-] — it INCLUDES the hyphen.
 * So for an entry like `/assets/index-C-4eASDL.js` the regex matched from the
 * hyphen inside the hash rather than from the one before it, leaving `4eASDL`,
 * six characters, under the eight it required. A perfectly good content hash was
 * rejected as "carries no content hash".
 *
 * Roughly one build in five draws such a hash, and it is NOT a flake: the digest
 * is a function of the source, so every run of that tree fails identically until
 * something unrelated changes. The failure surfaces as a build complaint, in the
 * one tool this repository uses to know whether its results are about its own
 * bytes at all.
 *
 * ── Why this is a widening and not a relaxation ────────────────────────────
 *
 * The check exists to reject a name that ANY build would produce — `index.js`,
 * `main.js` — because such a name proves nothing about whose bundle answered.
 * Adding '-' to the character class does not admit any of those: they contain no
 * hyphen at all, so there is no `-<8 or more>` tail to match. What it admits is
 * exactly the set that was always intended, and the test alongside asserts both
 * directions rather than only the one that was broken.
 */

/** The characters Vite's base64URL chunk digest can contain. */
export const HASH_ALPHABET = 'A-Za-z0-9_-'

/** Vite's default digest length. `{8,}` rather than `{8}` tolerates a longer one. */
export const HASH_MIN_LENGTH = 8

export const ENTRY_HASH_RE = new RegExp(`-[${HASH_ALPHABET}]{${HASH_MIN_LENGTH},}\\.js$`)

/**
 * True when `entry` ends in `-<digest>.js` with a digest of at least
 * HASH_MIN_LENGTH base64URL characters.
 */
export function hasContentHash(entry) {
  return typeof entry === 'string' && ENTRY_HASH_RE.test(entry)
}
