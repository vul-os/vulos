// appSearch.ts — the launcher's ranking function.
//
// Pure and React-free so it can be unit-tested on its own (the Spotlight
// component around it is all DOM). It ranks app entries against a typed query
// over the three fields an AppEntry actually carries: NAME, KEYWORDS and
// DESCRIPTION.
//
// Those three are NOT equal evidence, and pooling them into a single max()
// score gets the order wrong in a way that is easy to miss: fuzzyScore hands
// out a ~100_000 PREFIX bonus, so an app whose long prose DESCRIPTION happens
// to start with the query outranks an app whose NAME merely contains it. The
// fix is a TIER, not a weight — a name hit always beats a keyword hit, which
// always beats a description hit, and the fuzzy score only breaks ties inside
// a tier. Description matching is still there (searching "cpu" must surface
// Activity Monitor, whose name and description both omit the word), it just
// can never jump the queue.
import { fuzzyScore } from '../core/fuzzy'
import type { AppEntry } from './appTypes'

/** Match tiers, best first. Lower sorts earlier. */
export const TIER_NAME = 0
export const TIER_KEYWORD = 1
export const TIER_DESCRIPTION = 2
/** No field matched at all. */
export const TIER_NONE = 3

/** The subset of an AppEntry that ranking reads. Anything app-shaped works. */
export interface RankableApp {
  name: string
  keywords?: string[]
  description?: string
}

export interface AppMatch {
  /** TIER_NAME | TIER_KEYWORD | TIER_DESCRIPTION | TIER_NONE */
  tier: number
  /** Best fuzzy score within that tier; -Infinity when tier is TIER_NONE. */
  score: number
}

/**
 * Which field matched `query` best, and how well. The tier is what orders
 * results; the score only breaks ties inside a tier.
 */
export function matchApp(query: string, app: RankableApp): AppMatch {
  const nameScore = fuzzyScore(query, app.name)
  if (nameScore > -Infinity) return { tier: TIER_NAME, score: nameScore }

  let keywordBest = -Infinity
  for (const k of app.keywords || []) {
    const s = fuzzyScore(query, k)
    if (s > keywordBest) keywordBest = s
  }
  if (keywordBest > -Infinity) return { tier: TIER_KEYWORD, score: keywordBest }

  const descScore = fuzzyScore(query, app.description || '')
  if (descScore > -Infinity) return { tier: TIER_DESCRIPTION, score: descScore }

  return { tier: TIER_NONE, score: -Infinity }
}

/**
 * Rank apps against `query`, best first. Non-matching apps are dropped.
 * An empty/whitespace query returns [] — the launcher shows its grid instead.
 */
export function rankApps<T extends RankableApp>(query: string, apps: T[], limit?: number): T[] {
  const q = query.trim()
  if (!q) return []
  const scored: { app: T; tier: number; score: number }[] = []
  for (const app of apps) {
    const m = matchApp(q, app)
    if (m.tier === TIER_NONE) continue
    scored.push({ app, tier: m.tier, score: m.score })
  }
  scored.sort((a, b) => (a.tier - b.tier) || (b.score - a.score))
  const out = scored.map(s => s.app)
  return typeof limit === 'number' ? out.slice(0, limit) : out
}

export type { AppEntry }
