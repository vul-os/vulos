// useTicker.ts — the rail's single clock.
//
// ONE timer for the whole rail, not one per widget. A widget declares a cadence
// in its manifest and is handed a `now`; it never owns a timer, so the OS can
// see, align and stop every clock in the rail at once.
//
// The two cadences are separate STATE VALUES driven by the same scheduler, which
// is the part that matters for render cost: a rail holding a world clock (1 Hz)
// and eight minute-cadence tiles must re-render one tile per second, not nine. A
// single shared `now` would re-render all of them, and on a surface that sits
// under every window that is a per-second layout pass for nothing.
//
// The scheduler aligns to the wall-clock boundary rather than using a fixed
// interval. `setInterval(fn, 1000)` drifts — the callback fires a few ms late
// every time, the error accumulates, and after a while the displayed second
// changes visibly out of step with the system clock. Re-arming from
// `Date.now() % 1000` each tick pins it back to the boundary, and (unlike a
// fixed interval) it self-corrects after the browser throttles a background tab
// or the machine suspends.

import { useEffect, useState } from 'react'
import type { WidgetTick } from '../types'

export interface Ticker {
  /** Updates on every wall-clock second. */
  second: Date
  /** Updates on every wall-clock minute. */
  minute: Date
  /** Never updates — a stable value for tick:'none' widgets. */
  still: Date
}

/**
 * @param cadence the FINEST cadence any mounted widget asked for. 'none' stops
 *   the timer entirely: a rail of static widgets runs no timer at all.
 */
export function useTicker(cadence: WidgetTick): Ticker {
  const [second, setSecond] = useState(() => new Date())
  const [minute, setMinute] = useState(() => new Date())
  // A never-updated STATE value, not a ref: reading `ref.current` during render
  // is a render-phase read React explicitly warns against, and this value is
  // needed by the returned object on every render. `useState` with a lazy
  // initialiser gives the same "computed once, stable forever" semantics with
  // none of that problem.
  const [still] = useState(() => new Date())

  useEffect(() => {
    if (cadence === 'none') return
    const periodMs = cadence === 'second' ? 1000 : 60_000
    let timer: ReturnType<typeof setTimeout>
    let cancelled = false

    const arm = () => {
      // +8ms so the callback lands just AFTER the boundary. Firing exactly on it
      // races the clock: `new Date()` inside the callback can still read the
      // previous second, which makes the display appear to skip.
      const delay = periodMs - (Date.now() % periodMs) + 8
      timer = setTimeout(() => {
        if (cancelled) return
        const now = new Date()
        setSecond(now)
        // The minute value is only replaced when the minute actually changed, so
        // its referential identity is stable for 60 ticks and minute-cadence
        // tiles memoise cleanly against it.
        setMinute((prev) => (Math.floor(prev.getTime() / 60_000) === Math.floor(now.getTime() / 60_000) ? prev : now))
        arm()
      }, delay)
    }
    arm()
    return () => { cancelled = true; clearTimeout(timer) }
  }, [cadence])

  return { second, minute, still }
}

/** The finest cadence in a set of mounted widgets. */
export function finestTick(ticks: (WidgetTick | undefined)[]): WidgetTick {
  if (ticks.some((t) => t === 'second')) return 'second'
  if (ticks.some((t) => t === 'minute')) return 'minute'
  return 'none'
}

/** The `now` a widget with this manifest cadence should receive. */
export function nowFor(ticker: Ticker, tick: WidgetTick | undefined): Date {
  if (tick === 'second') return ticker.second
  if (tick === 'minute') return ticker.minute
  return ticker.still
}
