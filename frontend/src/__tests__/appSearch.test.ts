// appSearch.test.ts — the launcher's ranking contract (shell/appSearch.ts).
//
// The contract under test is the TIER: a NAME hit outranks a KEYWORD hit,
// which outranks a DESCRIPTION hit, and the raw fuzzy score only breaks ties
// inside a tier.
//
// This is not a cosmetic preference. fuzzyScore awards a ~100_000 PREFIX
// bonus, so a single pooled max() over all three fields lets an app whose
// long prose DESCRIPTION starts with the query leapfrog the app the user is
// actually named-searching for. Every ordering assertion below fails if the
// tiering is removed and the fields are pooled — verified by mutation.
import { describe, it, expect } from 'vitest'
import { rankApps, matchApp, TIER_NAME, TIER_KEYWORD, TIER_DESCRIPTION, TIER_NONE } from '../shell/appSearch'

// Verbatim registry entries (src/core/AppRegistry.ts) so the test moves if the
// real data moves.
const TERMINAL = {
  id: 'terminal',
  name: 'Terminal',
  description: 'System shell',
  keywords: ['terminal', 'shell', 'bash', 'sh', 'command', 'cli', 'console'],
}
const CLOCK = {
  id: 'clock',
  name: 'Clock',
  description: 'Time, world clocks, stopwatch, and timer',
  keywords: ['clock', 'time', 'alarm', 'stopwatch', 'timer'],
}
const ACTIVITY = {
  id: 'activity',
  name: 'Activity Monitor',
  description: 'System resource monitor',
  keywords: ['htop', 'cpu', 'ram', 'monitor', 'process', 'activity', 'task'],
}
const APPS = [CLOCK, ACTIVITY, TERMINAL]

describe('matchApp', () => {
  it('reports which field carried the match', () => {
    expect(matchApp('terminal', TERMINAL).tier).toBe(TIER_NAME)
    expect(matchApp('cpu', ACTIVITY).tier).toBe(TIER_KEYWORD)
    expect(matchApp('resource', ACTIVITY).tier).toBe(TIER_DESCRIPTION)
    expect(matchApp('zzzqqq', ACTIVITY).tier).toBe(TIER_NONE)
  })
})

describe('rankApps', () => {
  it('a NAME match outranks a description-prefix match on a different app', () => {
    // "system" is a PREFIX of Terminal's description ("System shell") — worth a
    // ~100_000 fuzzy bonus — but only a mid-string subsequence of the *name*
    // "Filesystem Tools". Pooling the fields would put Terminal first; the tier
    // puts the named app first, which is what a launcher user means.
    const filesystem = { id: 'fs', name: 'Filesystem Tools', description: 'Disk utilities', keywords: [] }
    const out = rankApps('system', [TERMINAL, filesystem])
    expect(out.map(a => a.id)).toEqual(['fs', 'terminal'])
  })

  it('a KEYWORD match outranks a description-EXACT match on a different app', () => {
    // Same shape one tier down. "shell" is an exact match for Konsole's terse
    // one-word description (fuzzyScore 1_000_000) but only a prefix of
    // Scripter's keyword "shellscript" (~100_189). Pooling the fields ranks
    // Konsole first on raw score; the tier ranks the keyword hit first.
    const scripter = { id: 'scripter', name: 'Scripter', description: 'Automate tasks', keywords: ['shellscript'] }
    const konsole = { id: 'konsole', name: 'Konsole', description: 'Shell', keywords: [] }
    const out = rankApps('shell', [konsole, scripter])
    expect(out.map(a => a.id)).toEqual(['scripter', 'konsole'])
  })

  it('"ter" puts Terminal first (its name), ahead of apps that only match by keyword or prose', () => {
    const out = rankApps('ter', APPS)
    expect(out[0]?.id).toBe('terminal')
    expect(out.findIndex(a => a.id === 'terminal')).toBeLessThan(out.findIndex(a => a.id === 'clock'))
  })

  it('finds an app by a KEYWORD its name and description never mention ("cpu" → Activity Monitor)', () => {
    expect(rankApps('cpu', APPS)[0]?.id).toBe('activity')
  })

  it('still finds an app by DESCRIPTION alone ("resource" → Activity Monitor)', () => {
    expect(rankApps('resource', APPS)[0]?.id).toBe('activity')
  })

  it('drops apps that match no field at all', () => {
    expect(rankApps('zzzqqq', APPS)).toEqual([])
  })

  it('returns nothing for an empty or whitespace query (the grid handles that case)', () => {
    expect(rankApps('', APPS)).toEqual([])
    expect(rankApps('   ', APPS)).toEqual([])
  })

  it('honours the result limit', () => {
    expect(rankApps('o', APPS, 2)).toHaveLength(2)
  })
})
