import { describe, it, expect, afterEach } from 'vitest'
import {
  parseScreenIdentity,
  isMultiScreen,
  screenWindowTitle,
  applyScreenWindowTitle,
} from '../screenIdentity'

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

// screenWindowTitle exists so labwc can tell two identical browser windows
// apart and place each on its own output. Its correctness is therefore a
// compositor concern, not a cosmetic one: if two instances carry the same
// title, one windowRule matches both and both land on one monitor.
describe('screenWindowTitle', () => {
  const base = 'Vulos'

  it('appends the connector name on a multi-screen kiosk', () => {
    const id = parseScreenIdentity('?screen=HDMI-A-1&screens=2&screenIndex=1')
    expect(screenWindowTitle(id, base)).toBe('Vulos — HDMI-A-1')
  })

  it('gives two screens DIFFERENT titles — the whole point', () => {
    const a = screenWindowTitle(parseScreenIdentity('?screen=HDMI-A-1&screens=2&screenIndex=1'), base)
    const b = screenWindowTitle(parseScreenIdentity('?screen=DP-2&screens=2&screenIndex=2'), base)
    expect(a).not.toBe(b)
  })

  it('leaves an ordinary tab alone', () => {
    expect(screenWindowTitle(null, base)).toBe(base)
  })

  it('leaves a single-screen kiosk alone', () => {
    const id = parseScreenIdentity('?screen=HDMI-A-1&screens=1&screenIndex=1')
    expect(screenWindowTitle(id, base)).toBe(base)
  })

  it('embeds exactly the connector name MoveToOutput expects', () => {
    // The title must contain the name verbatim: labwc's output attribute takes
    // "HDMI-A-1", and a decorated or truncated name would not match the rule.
    const id = parseScreenIdentity('?screen=DVI_D-1&screens=3&screenIndex=2')
    expect(screenWindowTitle(id, base)).toContain('DVI_D-1')
  })
})

// applyScreenWindowTitle is the single writer of document.title in this shell.
// screenWindowTitle above proves the STRING is right; these prove it actually
// reaches the document, which is the half that was missing when the first
// two-display boot found the second monitor black.
describe('applyScreenWindowTitle', () => {
  const SHIPPED_TITLE = 'Vulos — A sovereign, web-native operating system'

  const at = (search: string, title = SHIPPED_TITLE) => {
    window.history.replaceState({}, '', `/${search}`)
    document.title = title
    return applyScreenWindowTitle()
  }

  afterEach(() => {
    window.history.replaceState({}, '', '/')
    document.title = SHIPPED_TITLE
  })

  it('stamps the connector name onto the real document', () => {
    expect(at('?screen=Virtual-2&screens=2&screenIndex=2')).toBe('Vulos — Virtual-2')
    expect(document.title).toBe('Vulos — Virtual-2')
  })

  it('writes exactly the string labwc is told to match', () => {
    // The windowRule generated by scripts/vulos-kiosk-genconfig.sh is
    // title="Vulos — <connector>". One builder, one writer: if these two ever
    // disagree the rule matches nothing and the window is never moved.
    const id = parseScreenIdentity('?screen=Virtual-2&screens=2&screenIndex=2')
    expect(at('?screen=Virtual-2&screens=2&screenIndex=2')).toBe(screenWindowTitle(id, 'Vulos'))
  })

  it('gives the two windows of a two-screen boot different titles', () => {
    const first = at('?screen=Virtual-1&screens=2&screenIndex=1')
    const second = at('?screen=Virtual-2&screens=2&screenIndex=2')
    expect(first).not.toBe(second)
  })

  it('leaves an ordinary tab completely alone', () => {
    // No parameter at all: not merely "no connector name" but no write of any
    // kind — the title the HTML shipped with must survive untouched.
    expect(at('')).toBeNull()
    expect(document.title).toBe(SHIPPED_TITLE)
  })

  it('leaves a single-screen kiosk completely alone', () => {
    // The launcher passes screen= on the one-monitor path too, so this is a
    // real boot shape and not a hypothetical.
    expect(at('?screen=Virtual-1&screens=1&screenIndex=1')).toBeNull()
    expect(document.title).toBe(SHIPPED_TITLE)
  })

  it('refuses a hostile or half-formed parameter rather than titling with it', () => {
    expect(at('?screen=<script>&screens=2&screenIndex=1')).toBeNull()
    expect(at('?screen=Virtual-2&screens=2')).toBeNull()
    expect(document.title).toBe(SHIPPED_TITLE)
  })

  it('is idempotent — a second application does not stack names', () => {
    at('?screen=Virtual-2&screens=2&screenIndex=2')
    expect(applyScreenWindowTitle()).toBe('Vulos — Virtual-2')
    expect(document.title).toBe('Vulos — Virtual-2')
  })
})
