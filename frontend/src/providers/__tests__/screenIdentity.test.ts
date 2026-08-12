import { describe, it, expect } from 'vitest'
import { parseScreenIdentity, isMultiScreen } from '../screenIdentity'

// The screen identity arrives in a URL parameter, which is attacker-controlled
// by definition — anyone can type ?screen=whatever. It grants nothing, but it
// IS rendered into the shell chrome, so the parser is the only thing standing
// between a hostile string and the top bar.
//
// The contract that matters is that partial input is REFUSED rather than
// half-honoured. A chip reading "Screen 3 of 1" is worse than no chip, and the
// single-screen path — every hand-opened tab, every phone on the LAN, every
// one-monitor box — must behave exactly as it did before multi-screen existed.
describe('parseScreenIdentity', () => {
  it('accepts a complete, self-consistent identity', () => {
    expect(parseScreenIdentity('?screen=HDMI-A-1&screens=2&screenIndex=1')).toEqual({
      name: 'HDMI-A-1',
      index: 1,
      total: 2,
    })
  })

  // Each of these must yield null. Partial or inconsistent input is the whole
  // reason this function exists rather than three separate param reads.
  const refused: Array<[string, string]> = [
    ['no parameters at all (an ordinary tab)', ''],
    ['name only, no counts', '?screen=HDMI-A-1'],
    ['counts but no name', '?screens=2&screenIndex=1'],
    ['index greater than total — the "Screen 3 of 1" case', '?screen=DP-1&screens=1&screenIndex=3'],
    ['zero index (the ordering is 1-based)', '?screen=DP-1&screens=2&screenIndex=0'],
    ['non-numeric count', '?screen=DP-1&screens=two&screenIndex=1'],
    ['name with a space', '?screen=HDMI A 1&screens=2&screenIndex=1'],
    ['name with markup', '?screen=<img src=x>&screens=2&screenIndex=1'],
    ['name far longer than any DRM connector', `?screen=${'A'.repeat(64)}&screens=2&screenIndex=1`],
  ]
  for (const [why, search] of refused) {
    it(`refuses ${why}`, () => {
      expect(parseScreenIdentity(search)).toBeNull()
    })
  }

  it('accepts the real DRM connector shapes', () => {
    for (const n of ['HDMI-A-1', 'DP-2', 'eDP-1', 'Virtual-1', 'DVI_D-1']) {
      expect(parseScreenIdentity(`?screen=${n}&screens=2&screenIndex=2`)?.name).toBe(n)
    }
  })
})

describe('isMultiScreen', () => {
  // The launcher sets the parameter EITHER WAY so the two paths differ in as
  // few places as possible. So a single-screen kiosk carries a valid identity
  // with total=1, and must still be treated as an ordinary session: no chip.
  it('is false for a single-screen kiosk that carries the parameter', () => {
    expect(isMultiScreen(parseScreenIdentity('?screen=HDMI-A-1&screens=1&screenIndex=1'))).toBe(false)
  })
  it('is false for null (an ordinary tab)', () => {
    expect(isMultiScreen(null)).toBe(false)
  })
  it('is true only when more than one screen was launched', () => {
    expect(isMultiScreen(parseScreenIdentity('?screen=HDMI-A-1&screens=2&screenIndex=1'))).toBe(true)
  })
})
