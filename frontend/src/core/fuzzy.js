// fuzzy.js — the ⌘K command-palette scorer.
//
// A small, dependency-free subsequence matcher with word-boundary and
// consecutive-run bonuses, tuned for ranking short labels (app names, action
// titles, mail subjects) against a live-typed query. It is intentionally pure
// and side-effect free so it can be unit-tested in isolation and reused across
// every palette section.
//
// fuzzyScore(query, target) → number
//   • Returns a score where HIGHER is a better match.
//   • Returns -Infinity when `query` is not a subsequence of `target` (no match).
//   • An empty query returns 0 (neutral — "everything matches, rank elsewhere").
//
// The heuristics, roughly in order of weight:
//   • exact equality              → huge bonus
//   • prefix match                → large bonus
//   • word-boundary hits          → bonus per char that lands at the start of a
//                                    word (start-of-string, or after a space / -
//                                    / _ / . / / or a lower→upper camelCase step)
//   • consecutive matched chars   → escalating streak bonus
//   • gaps / leading offset       → small penalties (prefer tight, early matches)

const BOUNDARY_CHARS = new Set([' ', '-', '_', '.', '/', ':', '\t'])

function isBoundary(prevChar, char) {
  if (prevChar === undefined) return true // start of string
  if (BOUNDARY_CHARS.has(prevChar)) return true
  // camelCase / lower→UPPER transition (e.g. "openHome" → boundary at H)
  if (prevChar >= 'a' && prevChar <= 'z' && char >= 'A' && char <= 'Z') return true
  // letter→digit transition (e.g. "desktop2")
  if (!(prevChar >= '0' && prevChar <= '9') && char >= '0' && char <= '9') return true
  return false
}

/**
 * Score how well `query` fuzzy-matches `target`.
 * @param {string} query  the user-typed query
 * @param {string} target the candidate label
 * @returns {number} higher is better; -Infinity means no subsequence match
 */
export function fuzzyScore(query, target) {
  if (query == null || query === '') return 0
  if (target == null || target === '') return -Infinity

  const q = query.toLowerCase()
  const t = String(target)
  const tl = t.toLowerCase()

  // Fast paths ---------------------------------------------------------------
  if (tl === q) return 1_000_000
  if (tl.startsWith(q)) {
    // Strong prefix bonus; shorter targets rank above longer ones.
    return 100_000 + Math.max(0, 200 - t.length)
  }

  // Greedy left-to-right subsequence walk over the target ---------------------
  let qi = 0
  let score = 0
  let streak = 0
  let firstMatchIdx = -1
  let prevMatchIdx = -2

  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (tl[ti] !== q[qi]) continue

    if (firstMatchIdx === -1) firstMatchIdx = ti

    // Base per-matched-char credit.
    let charScore = 10

    // Word-boundary bonus.
    if (isBoundary(ti === 0 ? undefined : t[ti - 1], t[ti])) charScore += 18

    // Consecutive-run bonus (escalates so tight matches win).
    if (ti === prevMatchIdx + 1) {
      streak += 1
      charScore += 6 * streak
    } else {
      streak = 0
      // Gap penalty — the further from the previous match, the worse.
      if (prevMatchIdx >= 0) charScore -= Math.min(8, ti - prevMatchIdx - 1)
    }

    score += charScore
    prevMatchIdx = ti
    qi++
  }

  // Every query char must have been consumed for a valid match.
  if (qi < q.length) return -Infinity

  // Prefer matches that start earlier in the target.
  score -= firstMatchIdx * 0.5
  // Mild preference for shorter targets when scores are otherwise close.
  score -= t.length * 0.1

  return score
}

/**
 * Return whether `query` fuzzy-matches `target` at all.
 */
export function fuzzyMatches(query, target) {
  return fuzzyScore(query, target) > -Infinity
}

/**
 * Rank a list of items by their best fuzzy score across one or more keys.
 *
 * @param {string} query
 * @param {Array<any>} items
 * @param {(item:any) => (string|string[])} keyFn  returns the string(s) to score
 * @param {object} [opts]
 * @param {number} [opts.limit]  cap the number of returned items
 * @returns {Array<{item:any, score:number}>} matched items, best first
 */
export function fuzzyRank(query, items, keyFn, opts = {}) {
  const q = (query || '').trim()
  const scored = []
  for (const item of items) {
    const keys = keyFn(item)
    const list = Array.isArray(keys) ? keys : [keys]
    let best = -Infinity
    for (const k of list) {
      const s = fuzzyScore(q, k)
      if (s > best) best = s
    }
    if (best > -Infinity) scored.push({ item, score: best })
  }
  scored.sort((a, b) => b.score - a.score)
  return typeof opts.limit === 'number' ? scored.slice(0, opts.limit) : scored
}
