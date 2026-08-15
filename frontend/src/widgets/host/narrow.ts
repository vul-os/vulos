// narrow.ts — the trust boundary between the box's wire formats and a widget.
//
// Pure functions, deliberately in their own module (no JSX): every one of them
// takes an `unknown` that arrived from a socket, an HTTP response or a store and
// returns a typed, widget-facing shape. Keeping them here rather than beside the
// components means they are directly unit-testable and that a change to the
// box's field names is a change to exactly one file rather than to every widget.
//
// The rule they all follow: a field that is absent or the wrong type comes back
// ABSENT, never coerced. `Number(undefined)` is NaN and `NaN%` on the desktop is
// worse than a dash.

import type {
  WidgetCalendar, WidgetEvent, WidgetNotifications, WidgetTelemetry,
} from '../types'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}
function num(x: unknown): number | undefined {
  return typeof x === 'number' && Number.isFinite(x) ? x : undefined
}

/**
 * Narrow the telemetry frame.
 *
 * The socket payload is `unknown` by design in useTelemetry — it comes off the
 * wire. Widgets get a typed, renamed view instead of the raw frame.
 */
export function narrowTelemetry(stats: unknown, connected: boolean): WidgetTelemetry {
  if (!isRecord(stats)) return { connected }
  return {
    connected,
    cpu: num(stats.cpu),
    memPercent: num(stats.mem_percent),
    memUsedBytes: num(stats.mem_used),
    memTotalBytes: num(stats.mem_total),
    battery: num(stats.battery),
    charging: typeof stats.charging === 'boolean' ? stats.charging : undefined,
    tempC: num(stats.temp),
    uptime: typeof stats.uptime === 'string' ? stats.uptime : undefined,
    hostname: typeof stats.hostname === 'string' ? stats.hostname : undefined,
  }
}

interface RawEvent {
  id?: unknown
  title?: unknown
  _start?: unknown
  _end?: unknown
  allDay?: unknown
  location?: unknown
}

/** Map calendarApi's normalised event onto the widget-facing shape. */
export function narrowEvents(raw: unknown): WidgetEvent[] {
  if (!Array.isArray(raw)) return []
  const out: WidgetEvent[] = []
  for (let i = 0; i < raw.length; i++) {
    const e = raw[i] as RawEvent
    if (!isRecord(e)) continue
    const start = e._start instanceof Date && !Number.isNaN(e._start.getTime()) ? e._start : null
    // An event with no parseable start cannot be placed on a timeline, and
    // rendering it at the top with a blank time is worse than dropping it — the
    // widget would be asserting something about "next" that it does not know.
    if (!start) continue
    out.push({
      id: typeof e.id === 'string' && e.id ? e.id : `ev-${i}`,
      title: typeof e.title === 'string' ? e.title : '',
      start,
      end: e._end instanceof Date && !Number.isNaN(e._end.getTime()) ? e._end : null,
      allDay: e.allDay === true,
      location: typeof e.location === 'string' ? e.location : '',
    })
  }
  return out
}

/** The loading/unreachable/loaded triple, built from a settled read. */
export function calendarFromEvents(raw: unknown): WidgetCalendar {
  return { events: narrowEvents(raw), error: false }
}

/**
 * The read-only notification view.
 *
 * Only the newest few cross into a widget, and only title/body/read — never the
 * actions, the source preferences or the ids the shell uses to mutate them. A
 * widget granted `notifications` can DISPLAY what the box said; it cannot
 * dismiss anything, mark anything read, or enumerate history.
 */
export const NOTIFICATION_LIMIT = 4

interface StoreNotification { id?: unknown; title?: unknown; body?: unknown; read?: unknown }

export function narrowNotifications(items: unknown, unread: number): WidgetNotifications {
  const list = Array.isArray(items) ? items : []
  const recent = list.slice(0, NOTIFICATION_LIMIT).map((raw, i) => {
    const n = (isRecord(raw) ? raw : {}) as StoreNotification
    return {
      id: typeof n.id === 'string' && n.id ? n.id : `n-${i}`,
      title: typeof n.title === 'string' ? n.title : '',
      body: typeof n.body === 'string' ? n.body : '',
      read: n.read === true,
    }
  })
  return { recent, unread: Number.isFinite(unread) ? unread : 0 }
}
