// widgetLogic.test.ts — the decisions inside the builtin widgets.
//
// The rendering is checked with real pixels in e2e/widgets-rail.e2e.ts. What is
// checked here is the reasoning: which events count as "upcoming", which strings
// are a valid position, and how a price the user typed three weeks ago is
// described. Those have edge cases; a <span> does not.
import { describe, it, expect } from 'vitest'
import { ageLabel, changePct, parsePosition, readAsOf, upcomingFrom, WATCHLIST_AS_OF_KEY, WATCHLIST_RAW_KEY } from '../builtin/logic'
import { narrowEvents, narrowNotifications, narrowTelemetry } from '../host/narrow'
import type { WidgetEvent, WidgetStorage } from '../index'

function ev(over: Partial<WidgetEvent>): WidgetEvent {
  return { id: 'e', title: 't', start: null, end: null, allDay: false, location: '', ...over }
}

describe('upcomingFrom', () => {
  const now = new Date('2026-08-15T14:00:00Z')

  it('keeps an event you are currently IN', () => {
    // Filtering on start would drop the meeting the moment it begins, which is
    // exactly when you want to see it.
    const e = ev({ start: new Date('2026-08-15T13:30:00Z'), end: new Date('2026-08-15T15:00:00Z') })
    expect(upcomingFrom([e], now)).toHaveLength(1)
  })

  it('drops an event that has ended', () => {
    const e = ev({ start: new Date('2026-08-15T09:00:00Z'), end: new Date('2026-08-15T10:00:00Z') })
    expect(upcomingFrom([e], now)).toHaveLength(0)
  })

  it('treats an event with no end as ending at its start', () => {
    expect(upcomingFrom([ev({ start: new Date('2026-08-15T13:00:00Z') })], now)).toHaveLength(0)
    expect(upcomingFrom([ev({ start: new Date('2026-08-15T15:00:00Z') })], now)).toHaveLength(1)
  })

  it('keeps an ALL-DAY event for the whole of its day, not from midnight onward', () => {
    // A conference must not vanish from the rail at 00:01 on the day it happens.
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
    expect(upcomingFrom([ev({ start: today, allDay: true })], now)).toHaveLength(1)
    const yesterday = new Date(today.getTime() - 86_400_000)
    expect(upcomingFrom([ev({ start: yesterday, allDay: true })], now)).toHaveLength(0)
  })

  it('drops an event with no start rather than showing it with a blank time', () => {
    expect(upcomingFrom([ev({ start: null })], now)).toHaveLength(0)
  })

  it('sorts soonest first regardless of input order', () => {
    const later = ev({ id: 'b', start: new Date('2026-08-16T09:00:00Z') })
    const sooner = ev({ id: 'a', start: new Date('2026-08-15T18:00:00Z') })
    expect(upcomingFrom([later, sooner], now).map((e) => e.id)).toEqual(['a', 'b'])
  })
})

describe('parsePosition', () => {
  it('reads symbol, price and optional reference', () => {
    expect(parsePosition('AAPL 189.50 175.00')).toEqual({ symbol: 'AAPL', last: 189.5, ref: 175 })
    expect(parsePosition('msft 412.10')).toEqual({ symbol: 'MSFT', last: 412.1, ref: null })
    expect(parsePosition('  BRK.B   405.2  ')).toEqual({ symbol: 'BRK.B', last: 405.2, ref: null })
    expect(parsePosition('^GSPC 5400')).toEqual({ symbol: '^GSPC', last: 5400, ref: null })
  })

  it('refuses anything it cannot read rather than guessing', () => {
    // This is the one widget where a wrong number could cost someone money, so
    // an unreadable row is DROPPED and counted, never rendered as NaN or 0.
    for (const bad of ['', 'AAPL', 'AAPL abc', '189.50', 'A APL 1', '<script> 1', 'TOOLONGSYMBOL12 5']) {
      expect(parsePosition(bad), bad).toBeNull()
    }
    expect(parsePosition('AAPL Infinity')).toBeNull()
    expect(parsePosition('AAPL NaN')).toBeNull()
  })

  it('ignores an unreadable reference instead of failing the whole row', () => {
    expect(parsePosition('AAPL 189.50 junk')).toEqual({ symbol: 'AAPL', last: 189.5, ref: null })
  })
})

describe('changePct', () => {
  it('computes the move against the reference', () => {
    expect(changePct({ symbol: 'X', last: 110, ref: 100 })).toBeCloseTo(10, 6)
    expect(changePct({ symbol: 'X', last: 90, ref: 100 })).toBeCloseTo(-10, 6)
  })
  it('is null without a usable reference — never a division by zero', () => {
    expect(changePct({ symbol: 'X', last: 110, ref: null })).toBeNull()
    expect(changePct({ symbol: 'X', last: 110, ref: 0 })).toBeNull()
  })
})

describe('ageLabel', () => {
  const t = 1_700_000_000_000
  it('describes how stale the user\'s own figures are', () => {
    expect(ageLabel(t, t)).toBe('just now')
    expect(ageLabel(t, t + 90_000)).toBe('just now')
    expect(ageLabel(t, t + 5 * 60_000)).toBe('5 min ago')
    expect(ageLabel(t, t + 3 * 3_600_000)).toBe('3 hours ago')
    expect(ageLabel(t, t + 3_600_000)).toBe('1 hour ago')
    expect(ageLabel(t, t + 26 * 3_600_000)).toBe('1 day ago')
    expect(ageLabel(t, t + 21 * 86_400_000)).toBe('21 days ago')
  })
  it('never reports a negative age from a clock that went backwards', () => {
    expect(ageLabel(t, t - 100_000)).toBe('just now')
  })
})

describe('readAsOf', () => {
  function fakeStore(map: Record<string, string>): WidgetStorage {
    return {
      get: (k) => (k in map ? map[k] : null),
      set: () => true,
      remove: () => {},
      keys: () => Object.keys(map),
    }
  }

  it('returns the recorded timestamp when the figures are unchanged', () => {
    const s = fakeStore({ [WATCHLIST_RAW_KEY]: 'AAPL 1', [WATCHLIST_AS_OF_KEY]: '1234' })
    expect(readAsOf(s, 'AAPL 1', 9999)).toBe(1234)
  })
  it('returns "now" when the figures changed', () => {
    const s = fakeStore({ [WATCHLIST_RAW_KEY]: 'AAPL 1', [WATCHLIST_AS_OF_KEY]: '1234' })
    expect(readAsOf(s, 'MSFT 2', 9999)).toBe(9999)
  })
  it('returns "now" rather than a garbage timestamp', () => {
    const s = fakeStore({ [WATCHLIST_RAW_KEY]: 'AAPL 1', [WATCHLIST_AS_OF_KEY]: 'not a number' })
    expect(readAsOf(s, 'AAPL 1', 9999)).toBe(9999)
  })
  it('is null without storage', () => {
    expect(readAsOf(null, 'x', 1)).toBeNull()
  })
})

describe('narrowTelemetry', () => {
  it('renames the wire fields and drops the ones it cannot type', () => {
    const t = narrowTelemetry({ cpu: 12.5, mem_percent: 60, uptime: '3d', charging: true, temp: 'hot' }, true)
    expect(t).toMatchObject({ connected: true, cpu: 12.5, memPercent: 60, uptime: '3d', charging: true })
    // "hot" is not a temperature. Absent beats NaN°C on the desktop.
    expect(t.tempC).toBeUndefined()
  })
  it('survives a frame that is not an object', () => {
    expect(narrowTelemetry(null, false)).toEqual({ connected: false })
    expect(narrowTelemetry('nope', true)).toEqual({ connected: true })
  })
})

describe('narrowEvents', () => {
  it('drops rows with no parseable start', () => {
    const out = narrowEvents([
      { id: 'a', title: 'Real', _start: new Date('2026-08-15T10:00:00Z') },
      { id: 'b', title: 'Broken', _start: 'not a date' },
      { id: 'c', title: 'Broken too', _start: new Date('nonsense') },
      null,
      'garbage',
    ])
    expect(out.map((e) => e.id)).toEqual(['a'])
  })
  it('returns [] for a non-array', () => {
    expect(narrowEvents(null)).toEqual([])
    expect(narrowEvents({ events: [] })).toEqual([])
  })
})

describe('narrowNotifications', () => {
  it('exposes only title, body and read — never the mutation handles', () => {
    const out = narrowNotifications(
      [{ id: 'n1', title: 'A', body: 'b', read: false, actions: [{ id: 'nuke' }], prefs: { x: 1 } }],
      3,
    )
    expect(Object.keys(out.recent[0]).sort()).toEqual(['body', 'id', 'read', 'title'])
    expect(out.unread).toBe(3)
  })
  it('caps the list so a widget cannot enumerate history', () => {
    const many = Array.from({ length: 50 }, (_, i) => ({ id: `n${i}`, title: `T${i}` }))
    expect(narrowNotifications(many, 50).recent).toHaveLength(4)
  })
  it('survives garbage', () => {
    expect(narrowNotifications(null, NaN)).toEqual({ recent: [], unread: 0 })
  })
})
