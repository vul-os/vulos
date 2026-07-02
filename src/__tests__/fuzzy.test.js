import { describe, it, expect } from 'vitest'
import { fuzzyScore, fuzzyMatches, fuzzyRank } from '../core/fuzzy.js'

// The fuzzy scorer is the ranking heart of the ⌘K palette. These pin the
// contract: subsequence matching, no-match sentinel, and the bonus ordering
// (exact > prefix > word-boundary > scattered).

describe('fuzzyScore', () => {
  it('empty query is neutral (0), matching everything', () => {
    expect(fuzzyScore('', 'anything')).toBe(0)
  })

  it('returns -Infinity when the query is not a subsequence', () => {
    expect(fuzzyScore('xyz', 'Terminal')).toBe(-Infinity)
    expect(fuzzyScore('cab', 'abc')).toBe(-Infinity) // out of order
  })

  it('scores an exact match highest', () => {
    const exact = fuzzyScore('mail', 'Mail')
    const prefix = fuzzyScore('mai', 'Mail')
    expect(exact).toBeGreaterThan(prefix)
  })

  it('prefers a prefix match over a scattered subsequence', () => {
    const prefix = fuzzyScore('cal', 'Calculator')
    const scattered = fuzzyScore('cal', 'Critical Alarm') // c..al scattered
    expect(prefix).toBeGreaterThan(scattered)
  })

  it('rewards word-boundary (acronym) matches', () => {
    // "am" hits the start of Activity + Monitor.
    const boundary = fuzzyScore('am', 'Activity Monitor')
    // "am" mid-word in a single token.
    const midWord = fuzzyScore('am', 'Diagram')
    expect(boundary).toBeGreaterThan(midWord)
  })

  it('matches case-insensitively', () => {
    expect(fuzzyScore('TERM', 'terminal')).toBeGreaterThan(-Infinity)
    expect(fuzzyScore('term', 'TERMINAL')).toBeGreaterThan(-Infinity)
  })

  it('handles camelCase boundaries', () => {
    expect(fuzzyMatches('oh', 'openHome')).toBe(true)
    // "oh" should score better than a non-boundary "pe".
    expect(fuzzyScore('oh', 'openHome')).toBeGreaterThan(fuzzyScore('pe', 'openHome'))
  })
})

describe('fuzzyRank', () => {
  const apps = [
    { id: 'terminal', name: 'Terminal', keywords: ['shell', 'bash'] },
    { id: 'calc', name: 'Calculator', keywords: ['math'] },
    { id: 'calendar', name: 'Calendar', keywords: ['events', 'schedule'] },
    { id: 'mail', name: 'Mail', keywords: ['email', 'inbox'] },
  ]

  it('ranks the best match first', () => {
    const res = fuzzyRank('cal', apps, a => [a.name, ...a.keywords])
    expect(res[0].item.id === 'calc' || res[0].item.id === 'calendar').toBe(true)
    // Terminal must not appear — no "cal" subsequence.
    expect(res.find(r => r.item.id === 'terminal')).toBeUndefined()
  })

  it('matches on keyword keys as well as name', () => {
    const res = fuzzyRank('inbox', apps, a => [a.name, ...a.keywords])
    expect(res[0].item.id).toBe('mail')
  })

  it('respects the limit option', () => {
    const res = fuzzyRank('a', apps, a => [a.name, ...a.keywords], { limit: 2 })
    expect(res.length).toBeLessThanOrEqual(2)
  })

  it('returns an empty array when nothing matches', () => {
    const res = fuzzyRank('zzzz', apps, a => [a.name])
    expect(res).toEqual([])
  })
})
