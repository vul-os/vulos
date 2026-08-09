/**
 * CalendarWidget — an ambient, always-on Calendar widget pinned to the
 * right-hand side of the desktop (macOS-style). It answers "what's next today"
 * at a glance without launching an app, and expands on click into the week's
 * agenda. Clicking the header (or "Open Calendar") launches the full standalone
 * Calendar app (the `vulos-calendar` builtin over lilmail's /v1).
 *
 * DATA: it reads the SAME PIM seam the standalone Calendar app uses —
 * GET /api/pim/calendar/events (via the box's credential-brokering proxy to
 * lilmail's /v1 calendar) — through the shared calendarApi.listEvents(). This
 * fully decouples the widget from the assistant Home aggregate
 * (/api/assistant/home): the agenda no longer depends on the assistant service
 * being up, only on the mail/calendar backend. The read is scoped to the
 * caller's own session (the proxy 401s otherwise), so it can never surface
 * another account's events.
 *
 * DEGRADES HONESTLY: when the mail/calendar backend is unconfigured or
 * unreachable, the widget shows a "Connect Mail" / "Calendar unavailable"
 * state — never a crash, never a skeleton forever, and never invented events.
 * Every date value is parsed defensively (in calendarApi) so a malformed event
 * can't white-screen the shell.
 */
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getAppById } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { listEvents } from '../builtin/calendar/calendarApi'
import type { ShellContextValue } from '../providers/ShellProvider'
import './shell-chrome.css'

const DAY_MS = 24 * 60 * 60 * 1000

// builtin/calendar/calendarApi.js (untyped, out of scope) — normalizeEvent()'s
// real return shape, restated here so this widget (the sole shell consumer of
// listEvents()) gets a real type instead of an inferred `any[]`.
interface CalendarEvent {
  id: string
  title: string
  start: string
  end: string
  _start: Date | null
  _end: Date | null
  location: string
  notes: string
  allDay: boolean
}

// ── display helpers ──────────────────────────────────────────────────────────
function fmtTime(d: Date | null): string {
  return d ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : ''
}
function isSameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}
function relDay(d: Date | null, now: Date): string {
  if (!d) return ''
  if (isSameDay(d, now)) return 'Today'
  const tm = new Date(now); tm.setDate(tm.getDate() + 1)
  if (isSameDay(d, tm)) return 'Tomorrow'
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
}
// Event's display time: all-day → "All day", else HH:MM. Guards a null date.
function eventWhen(ev: CalendarEvent): string {
  if (ev.allDay) return 'All day'
  return fmtTime(ev._start) || '—'
}

// Module-scoped cache so the widget paints its last-known agenda instantly on
// remount (it lives on the desktop, which tears down/rebuilds) and refreshes in
// the background — no skeleton flash on every return to the desktop.
let cachedEvents: CalendarEvent[] | null = null

// `embedded` drops the self-pinning so the widget can be stacked inside a
// column that does the positioning (DesktopWidgets). Left off, the widget keeps
// pinning itself to the top-right exactly as before — every existing call site,
// and CalendarWidget.test.tsx, renders it that way.
export default function CalendarWidget({ embedded = false }: { embedded?: boolean } = {}) {
  const { openWindow } = useShell()
  const [events, setEvents] = useState<CalendarEvent[]>(cachedEvents || [])
  const [loading, setLoading] = useState(!cachedEvents)
  // fresh = the last read succeeded (the agenda is live, even if empty).
  const [fresh, setFresh] = useState(!!cachedEvents)
  const [error, setError] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [now, setNow] = useState(() => new Date())

  // Fetch never flips `loading` on synchronously — initial loading is derived
  // from the cache (useState above), and every path clears it in .finally. This
  // keeps the mount effect free of a synchronous setState (no cascading render)
  // and means the 5-min background refresh doesn't flash a skeleton.
  const load = useCallback(() => {
    // Read today → +8 days so "the week ahead" is covered with a day of slack.
    const from = new Date(); from.setHours(0, 0, 0, 0)
    const to = new Date(from.getTime() + 8 * DAY_MS)
    listEvents(from, to)
      .then((evs: CalendarEvent[]) => { cachedEvents = evs; setEvents(evs); setFresh(true); setError(false) })
      .catch(() => { setFresh(false); setError(true) })
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])
  // Refresh the agenda every 5 min, and tick "now" every minute so the
  // next-up computation and the clock stay honest without a reload.
  useEffect(() => {
    const a = setInterval(load, 5 * 60 * 1000)
    const c = setInterval(() => setNow(new Date()), 60 * 1000)
    return () => { clearInterval(a); clearInterval(c) }
  }, [load])

  const openCalendar = useCallback(() => {
    const app = getAppById('vulos-calendar')
    if (app) launchApp(app, { openWindow })
  }, [openWindow])

  // "What's next": events that haven't ended yet (all-day today+ counts).
  // calendarApi already parsed + sorted them by start ascending.
  const upcoming = useMemo(() => {
    const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate())
    return events.filter((ev) => {
      if (!ev._start) return false
      if (ev.allDay) return ev._start >= midnight
      const end = ev._end || ev._start
      return end >= now
    })
  }, [events, now])

  const next = upcoming[0] || null
  const hasError = error
  // "Not configured" — the read failed AND there is nothing cached to show.
  const notConfigured = hasError && events.length === 0

  return (
    <div
      className={
        embedded
          ? 'w-full select-none'
          : 'fixed right-3 z-30 w-60 max-w-[calc(100vw-1.5rem)] select-none'
      }
      style={embedded ? undefined : { top: '2.75rem' }}
      data-calendar-widget
    >
      <div className="vshell-surface rounded-2xl overflow-hidden">
        {/* Header — date + live/stale dot; whole row toggles expand */}
        <button
          type="button"
          onClick={() => setExpanded(v => !v)}
          aria-expanded={expanded}
          aria-label={expanded ? 'Collapse calendar' : 'Expand calendar'}
          className="vshell-row w-full flex items-center justify-between gap-2 px-3.5 py-2.5 text-left focus-primary"
        >
          <div className="min-w-0">
            <div className="text-[12px] font-mono uppercase tracking-[0.16em]" style={{ color: 'var(--text-faint)' }}>
              {now.toLocaleDateString(undefined, { weekday: 'long' })}
            </div>
            <div className="text-[15px] font-semibold leading-tight" style={{ color: 'var(--text-primary)' }}>
              {now.toLocaleDateString(undefined, { month: 'long', day: 'numeric' })}
            </div>
          </div>
          <span
            className="flex items-center gap-1 text-[12px] font-mono uppercase tracking-wider shrink-0"
            style={{ color: 'var(--text-faint)' }}
            title={fresh ? 'Calendar is live' : hasError ? 'Calendar unavailable' : ''}
          >
            {!loading && !notConfigured && (
              <>
                <span
                  className="inline-block w-1.5 h-1.5 rounded-full"
                  style={{ background: fresh ? 'var(--status-success)' : 'var(--status-danger)' }}
                />
                {fresh ? 'live' : 'stale'}
              </>
            )}
          </span>
        </button>

        {/* Next-up strip — the always-visible "what's next" answer */}
        <div className="px-3.5 pb-2.5">
          {loading && events.length === 0 ? (
            <div className="h-8 rounded-lg animate-pulse" style={{ background: 'color-mix(in srgb, var(--bg-hover) 60%, transparent)' }} />
          ) : notConfigured ? (
            <div className="text-[12px] leading-snug" style={{ color: 'var(--text-muted)' }}>
              <div>Calendar unavailable.</div>
              <button
                type="button"
                onClick={() => openApp_connectMail(openWindow)}
                className="mt-1 text-[12px] font-mono transition-colors focus-primary rounded"
                style={{ color: 'var(--accent)' }}
              >
                Connect Mail →
              </button>
            </div>
          ) : next ? (
            <button
              type="button"
              onClick={openCalendar}
              className="vshell-row w-full flex items-center gap-2.5 rounded-lg text-left -mx-1 px-1 py-1 focus-primary"
            >
              <div className="w-14 shrink-0 text-right">
                <div className="text-[12px] font-mono" style={{ color: 'var(--accent)' }}>{eventWhen(next)}</div>
                <div className="text-[12px] font-mono" style={{ color: 'var(--text-faint)' }}>{relDay(next._start, now)}</div>
              </div>
              <div className="w-px self-stretch vshell-hairline" />
              <div className="min-w-0">
                <div className="text-[12.5px] truncate" style={{ color: 'var(--text-primary)' }}>{next.title || '(untitled)'}</div>
                {next.location && <div className="text-[12px] truncate" style={{ color: 'var(--text-muted)' }}>{next.location}</div>}
              </div>
            </button>
          ) : (
            <div className="text-[12px]" style={{ color: 'var(--text-muted)' }}>Nothing on your calendar.</div>
          )}
        </div>

        {/* Expanded agenda — the week ahead */}
        {expanded && (
          <div className="vshell-border-t max-h-72 overflow-y-auto">
            {notConfigured ? (
              <div className="px-3.5 py-3 text-[12px]" style={{ color: 'var(--text-muted)' }}>
                Connect Mail to see your agenda here.
              </div>
            ) : upcoming.length <= 1 ? (
              <div className="px-3.5 py-3 text-[12px]" style={{ color: 'var(--text-muted)' }}>
                {upcoming.length === 0 ? 'Nothing on your calendar for the week ahead.' : 'No further events this week.'}
              </div>
            ) : (
              <ul className="py-1">
                {upcoming.slice(1).map((ev, i) => (
                  <li key={ev.id || i}>
                    <button
                      type="button"
                      onClick={openCalendar}
                      className="vshell-row w-full flex items-center gap-2.5 px-3.5 py-1.5 text-left focus-primary"
                    >
                      <div className="w-14 shrink-0 text-right">
                        <div className="text-[12px] font-mono" style={{ color: 'var(--text-secondary)' }}>{eventWhen(ev)}</div>
                        <div className="text-[12px] font-mono" style={{ color: 'var(--text-faint)' }}>{relDay(ev._start, now)}</div>
                      </div>
                      <div className="w-px self-stretch vshell-hairline" />
                      <div className="min-w-0">
                        <div className="text-[12px] truncate" style={{ color: 'var(--text-primary)' }}>{ev.title || '(untitled)'}</div>
                        {ev.location && <div className="text-[12px] truncate" style={{ color: 'var(--text-muted)' }}>{ev.location}</div>}
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="vshell-border-t px-3.5 py-2">
              <button
                type="button"
                onClick={openCalendar}
                className="text-[12px] font-mono transition-colors focus-primary rounded"
                style={{ color: 'var(--text-muted)' }}
              >
                Open Calendar →
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// Opening Mail lets the user configure the account that backs the calendar.
function openApp_connectMail(openWindow: ShellContextValue['openWindow']) {
  const app = getAppById('lilmail')
  if (app) launchApp(app, { openWindow })
}
