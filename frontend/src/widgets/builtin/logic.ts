// logic.ts — the pure, testable parts of the builtin widgets.
//
// Separated from the .tsx files for two reasons that happen to agree:
//
//  - These are the assertions worth writing. "Does an all-day event still count
//    as upcoming at 00:01" and "is `AAPL 189.5.2` rejected" are decisions with
//    edge cases; "does a <span> render" is not. Keeping them here means the
//    tests need no DOM and run in milliseconds.
//  - A module that exports components and non-components at once breaks React
//    Fast Refresh (react-refresh/only-export-components), so each widget file
//    exports exactly its component and its helpers live here.

// From the PUBLIC entry, not '../types'. A widget author has no reason to know
// that types.ts exists, so neither does a builtin — publicApi.test.ts caught
// this exact import and it is the whole point of that gate.
import type { WidgetEvent, WidgetStorage } from '../index'

// ── agenda ───────────────────────────────────────────────────────────────────

/**
 * Events that have not ended yet, soonest first.
 *
 * An ALL-DAY event counts for the whole of its day rather than from midnight
 * onward, so a conference does not vanish from the rail at 00:01 on the day it
 * is happening. A timed event counts until its END, not its start, so you can
 * still see the meeting you are currently in.
 */
export function upcomingFrom(events: WidgetEvent[], now: Date): WidgetEvent[] {
  const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  return events
    .filter((ev) => {
      if (!ev.start) return false
      if (ev.allDay) return ev.start >= midnight
      return (ev.end ?? ev.start) >= now
    })
    .sort((a, b) => (a.start?.getTime() ?? 0) - (b.start?.getTime() ?? 0))
}

// ── watchlist ────────────────────────────────────────────────────────────────

export interface Position {
  symbol: string
  last: number
  ref: number | null
}

/**
 * Parse one "SYMBOL LAST [REF]" entry.
 *
 * Returns null for anything it cannot read rather than guessing. A watchlist row
 * showing NaN, or showing a price the user did not type, is worse than a row
 * that is absent — this is the one widget where a wrong number could cost
 * someone money.
 */
export function parsePosition(raw: string): Position | null {
  if (typeof raw !== 'string') return null
  const parts = raw.trim().split(/\s+/)
  if (parts.length < 2) return null
  const symbol = parts[0].toUpperCase()
  // Tickers are letters, digits, dot, dash and caret (BRK.B, RIO-L, ^GSPC).
  // The caret is allowed to LEAD as well as follow: index symbols are written
  // `^GSPC`, and a first-character class that omitted it rejected every index
  // while the comment claimed they were supported.
  // Anything else is not a symbol and is refused rather than rendered as-is.
  if (!/^[A-Z0-9^][A-Z0-9.\-^]{0,11}$/.test(symbol)) return null
  const last = Number(parts[1])
  // Number('') is 0 and Number(' ') is 0, which would render a real-looking
  // price of zero; Number.isFinite also rejects Infinity and NaN.
  if (!Number.isFinite(last) || parts[1] === '') return null
  const ref = parts.length > 2 ? Number(parts[2]) : NaN
  return { symbol, last, ref: Number.isFinite(ref) ? ref : null }
}

/** Percent change from ref to last, or null when there is no usable reference. */
export function changePct(p: Position): number | null {
  if (p.ref === null || p.ref === 0) return null
  return ((p.last - p.ref) / p.ref) * 100
}

/** "just now" / "2 hours ago" / "3 days ago" — how stale the user's own figures are. */
export function ageLabel(thenMs: number, nowMs: number): string {
  const d = Math.max(0, nowMs - thenMs)
  const min = Math.floor(d / 60_000)
  if (min < 2) return 'just now'
  if (min < 60) return `${min} min ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} hour${hr === 1 ? '' : 's'} ago`
  const day = Math.floor(hr / 24)
  return `${day} day${day === 1 ? '' : 's'} ago`
}

export const WATCHLIST_RAW_KEY = 'raw'
export const WATCHLIST_AS_OF_KEY = 'asOf'

/** Read the recorded timestamp, or "now" if these figures are new/unseen. */
export function readAsOf(storage: WidgetStorage | null, joined: string, now: number): number | null {
  if (!storage) return null
  if (storage.get(WATCHLIST_RAW_KEY) !== joined) return now
  const stored = Number(storage.get(WATCHLIST_AS_OF_KEY))
  return Number.isFinite(stored) && stored > 0 ? stored : now
}
