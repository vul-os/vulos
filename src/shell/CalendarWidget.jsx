/**
 * CalendarWidget — an ambient, always-on Calendar widget pinned to the
 * right-hand side of the desktop (macOS-style). It answers "what's next today"
 * at a glance without launching an app, and expands on click into the week's
 * agenda. Clicking the header (or "Open Calendar") launches the full Calendar
 * surface (the Mail product's /calendar view via `vulos-calendar`).
 *
 * DATA: it reads the SAME on-box aggregate the Home surface uses —
 * GET /api/assistant/home — and consumes only its calendar fields:
 *   agenda        []{ title, start, end, location, all_day }  (today → +7d)
 *   agenda_fresh  bool  — the read succeeded (may still be empty)
 *   agenda_error  string — why the read failed (mail/cal backend unreachable)
 * The agenda is a pure on-box /v1 calendar read (no new egress) scoped to the
 * caller's own session — it can never surface another account's events.
 *
 * DEGRADES HONESTLY: when the mail/calendar backend is unconfigured or
 * unreachable, the widget shows a "Connect Mail" / "Calendar unavailable"
 * state — never a crash, never a skeleton forever, and never invented events.
 * Every date value is parsed defensively so a malformed event can't white-screen
 * the shell.
 */
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getAppById } from '../core/AppRegistry'
import { launchApp } from './launchApp'

// Module-scoped cache so the widget paints its last-known agenda instantly on
// remount (it lives on the desktop, which tears down/rebuilds) and refreshes in
// the background — no skeleton flash on every return to the desktop.
let cachedHome = null

const reduceMotion = () =>
  typeof window !== 'undefined' &&
  window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

// ── defensive date helpers (never throw on a bad ISO string) ─────────────────
function parseDate(iso) {
  if (!iso) return null
  const d = new Date(iso)
  return isNaN(d.getTime()) ? null : d
}
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
function eventWhen(ev, d) {
  if (ev.all_day) return 'All day'
  return fmtTime(d) || '—'
}

export default function CalendarWidget() {
  const { openWindow } = useShell()
  const [data, setData] = useState(cachedHome)
  const [loading, setLoading] = useState(!cachedHome)
  const [offline, setOffline] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [now, setNow] = useState(() => new Date())

  // Fetch never flips `loading` on synchronously — initial loading is derived
  // from the cache (useState above), and every path clears it in .finally. This
  // keeps the mount effect free of a synchronous setState (no cascading render)
  // and means the 5-min background refresh doesn't flash a skeleton.
  const load = useCallback(() => {
    fetch('/api/assistant/home', { credentials: 'include' })
      .then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json() })
      .then(d => { cachedHome = d; setData(d); setOffline(false) })
      .catch(() => setOffline(true))
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

  // Normalise the raw agenda into parsed, sorted, still-relevant events. An
  // event with an unparseable start is dropped rather than rendered blank.
  const events = useMemo(() => {
    const raw = Array.isArray(data?.agenda) ? data.agenda : []
    return raw
      .map(ev => ({ ...ev, _d: parseDate(ev.start) }))
      .filter(ev => ev._d)
      .sort((a, b) => a._d - b._d)
  }, [data])

  // "What's next": the soonest event that hasn't ended (or all-day today+).
  const upcoming = useMemo(() => {
    return events.filter(ev => {
      if (ev.all_day) return ev._d >= new Date(now.getFullYear(), now.getMonth(), now.getDate())
      const end = parseDate(ev.end) || ev._d
      return end >= now
    })
  }, [events, now])

  const next = upcoming[0] || null
  const agendaFresh = data?.agenda_fresh
  const hasError = !!data?.agenda_error || offline
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
            title={agendaFresh ? 'Calendar is live' : hasError ? 'Calendar unavailable' : ''}
          >
            {data && !notConfigured && (
              <>
                <span
                  className="inline-block w-1.5 h-1.5 rounded-full"
                  style={{ background: agendaFresh ? 'var(--status-success)' : 'var(--status-danger)' }}
                />
                {agendaFresh ? 'live' : 'stale'}
              </>
            )}
          </span>
        </button>

        {/* Next-up strip — the always-visible "what's next" answer */}
        <div className="px-3.5 pb-2.5">
          {loading && !data ? (
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
                <div className="text-[12px] font-mono text-neutral-200">{eventWhen(next, next._d)}</div>
                <div className="text-[9px] font-mono text-neutral-600">{relDay(next._d, now)}</div>
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
          <div
            className="border-t border-neutral-800/60 max-h-72 overflow-y-auto"
            style={reduceMotion() ? undefined : { animation: 'none' }}
          >
            {upcoming.length <= 1 && !notConfigured ? (
              <div className="px-3.5 py-3 text-[12px] text-neutral-500">
                {upcoming.length === 0 ? 'Nothing on your calendar for the week ahead.' : 'No further events this week.'}
              </div>
            ) : notConfigured ? (
              <div className="px-3.5 py-3 text-[12px] text-neutral-500">
                Connect Mail to see your agenda here.
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
                        <div className="text-[11.5px] font-mono text-neutral-300">{eventWhen(ev, ev._d)}</div>
                        <div className="text-[9px] font-mono text-neutral-600">{relDay(ev._d, now)}</div>
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
