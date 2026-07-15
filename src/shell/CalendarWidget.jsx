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

const DAY_MS = 24 * 60 * 60 * 1000

// Module-scoped cache so the widget paints its last-known agenda instantly on
// remount (it lives on the desktop, which tears down/rebuilds) and refreshes in
// the background — no skeleton flash on every return to the desktop.
let cachedEvents = null

// ── display helpers ──────────────────────────────────────────────────────────
function fmtTime(d) {
  return d ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : ''
}
function isSameDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}
function relDay(d, now) {
  if (!d) return ''
  if (isSameDay(d, now)) return 'Today'
  const tm = new Date(now); tm.setDate(tm.getDate() + 1)
  if (isSameDay(d, tm)) return 'Tomorrow'
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
}
// Event's display time: all-day → "All day", else HH:MM. Guards a null date.
function eventWhen(ev) {
  if (ev.allDay) return 'All day'
  return fmtTime(ev._start) || '—'
}

export default function CalendarWidget() {
  const { openWindow } = useShell()
  const [events, setEvents] = useState(cachedEvents || [])
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
      .then((evs) => { cachedEvents = evs; setEvents(evs); setFresh(true); setError(false) })
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
      className="fixed right-3 z-30 w-60 select-none"
      style={{ top: '2.75rem' }}
      data-calendar-widget
    >
      <div className="rounded-2xl border border-neutral-800/70 bg-neutral-900/80 backdrop-blur-xl shadow-xl shadow-black/40 overflow-hidden">
        {/* Header — date + live/stale dot; whole row toggles expand */}
        <button
          type="button"
          onClick={() => setExpanded(v => !v)}
          aria-expanded={expanded}
          aria-label={expanded ? 'Collapse calendar' : 'Expand calendar'}
          className="w-full flex items-center justify-between gap-2 px-3.5 py-2.5 text-left hover:bg-neutral-800/40 transition-colors focus-primary"
        >
          <div className="min-w-0">
            <div className="text-[10px] font-mono uppercase tracking-[0.16em] text-neutral-500">
              {now.toLocaleDateString(undefined, { weekday: 'long' })}
            </div>
            <div className="text-[15px] font-semibold text-neutral-100 leading-tight">
              {now.toLocaleDateString(undefined, { month: 'long', day: 'numeric' })}
            </div>
          </div>
          <span
            className="flex items-center gap-1 text-[9px] font-mono uppercase tracking-wider text-neutral-600 shrink-0"
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
            <div className="h-8 rounded-lg bg-neutral-800/50 animate-pulse" />
          ) : notConfigured ? (
            <div className="text-[12px] text-neutral-500 leading-snug">
              <div>Calendar unavailable.</div>
              <button
                type="button"
                onClick={() => openApp_connectMail(openWindow)}
                className="mt-1 text-[11px] font-mono text-neutral-400 hover:text-neutral-200 transition-colors"
              >
                Connect Mail →
              </button>
            </div>
          ) : next ? (
            <button
              type="button"
              onClick={openCalendar}
              className="w-full flex items-center gap-2.5 rounded-lg text-left hover:bg-neutral-800/40 -mx-1 px-1 py-1 transition-colors focus-primary"
            >
              <div className="w-14 shrink-0 text-right">
                <div className="text-[12px] font-mono text-neutral-200">{eventWhen(next)}</div>
                <div className="text-[9px] font-mono text-neutral-600">{relDay(next._start, now)}</div>
              </div>
              <div className="w-px self-stretch bg-neutral-800" />
              <div className="min-w-0">
                <div className="text-[12.5px] text-neutral-100 truncate">{next.title || '(untitled)'}</div>
                {next.location && <div className="text-[10.5px] text-neutral-500 truncate">{next.location}</div>}
              </div>
            </button>
          ) : (
            <div className="text-[12px] text-neutral-500">Nothing on your calendar.</div>
          )}
        </div>

        {/* Expanded agenda — the week ahead */}
        {expanded && (
          <div className="border-t border-neutral-800/60 max-h-72 overflow-y-auto">
            {notConfigured ? (
              <div className="px-3.5 py-3 text-[12px] text-neutral-500">
                Connect Mail to see your agenda here.
              </div>
            ) : upcoming.length <= 1 ? (
              <div className="px-3.5 py-3 text-[12px] text-neutral-500">
                {upcoming.length === 0 ? 'Nothing on your calendar for the week ahead.' : 'No further events this week.'}
              </div>
            ) : (
              <ul className="py-1">
                {upcoming.slice(1).map((ev, i) => (
                  <li key={ev.id || i}>
                    <button
                      type="button"
                      onClick={openCalendar}
                      className="w-full flex items-center gap-2.5 px-3.5 py-1.5 text-left hover:bg-neutral-800/40 transition-colors focus-primary"
                    >
                      <div className="w-14 shrink-0 text-right">
                        <div className="text-[11.5px] font-mono text-neutral-300">{eventWhen(ev)}</div>
                        <div className="text-[9px] font-mono text-neutral-600">{relDay(ev._start, now)}</div>
                      </div>
                      <div className="w-px self-stretch bg-neutral-800" />
                      <div className="min-w-0">
                        <div className="text-[12px] text-neutral-100 truncate">{ev.title || '(untitled)'}</div>
                        {ev.location && <div className="text-[10px] text-neutral-500 truncate">{ev.location}</div>}
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="border-t border-neutral-800/60 px-3.5 py-2">
              <button
                type="button"
                onClick={openCalendar}
                className="text-[11px] font-mono text-neutral-500 hover:text-neutral-300 transition-colors focus-primary"
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
function openApp_connectMail(openWindow) {
  const app = getAppById('lilmail')
  if (app) launchApp(app, { openWindow })
}
