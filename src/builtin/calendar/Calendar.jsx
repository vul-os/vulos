/**
 * Calendar — the OS's standalone Calendar app (the "GNOME Calendar" of Vulos).
 *
 * A thin, self-contained surface over the box's PIM proxy (/api/pim/calendar/*),
 * which brokers to lilmail's /v1 calendar (CalDAV + any OAuth-connected Google/
 * Outlook calendars lilmail aggregates). It owns NO storage of its own — lilmail
 * is the Evolution-Data-Server; this is just the view.
 *
 * Views: a month grid and a linear agenda. Full event CRUD via a small editor
 * modal (create / edit / delete). Every date is parsed defensively so a malformed
 * event can never white-screen the app, and a backend failure degrades to an
 * honest "Calendar unavailable — Connect Mail" state (lilmail configures the
 * account, incl. the OAuth "online accounts" connect).
 */
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { launchApp } from '../../shell/launchApp'
import { listEvents, createEvent, updateEvent, deleteEvent, parseDate } from './calendarApi'

const DAY_MS = 24 * 60 * 60 * 1000
const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

function startOfDay(d) { const x = new Date(d); x.setHours(0, 0, 0, 0); return x }
function isSameDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}
function addMonths(d, n) { const x = new Date(d); x.setMonth(x.getMonth() + n); return x }

// The 6-week (42-cell) grid start: the Sunday on/before the 1st of the month.
function gridStart(month) {
  const first = new Date(month.getFullYear(), month.getMonth(), 1)
  return startOfDay(new Date(first.getTime() - first.getDay() * DAY_MS))
}

// Local <input type=datetime-local> value ("YYYY-MM-DDTHH:MM") for a Date.
function toLocalInput(d) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
function toDateInput(d) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
function fmtTime(d) {
  return d ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : ''
}
function fmtDayLabel(d, now) {
  if (isSameDay(d, now)) return 'Today'
  const tm = new Date(now.getTime() + DAY_MS)
  if (isSameDay(d, tm)) return 'Tomorrow'
  return d.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' })
}

// A blank editor form seeded at `day` (defaults to a 1-hour slot at the next hour).
function blankEvent(day) {
  const base = day ? new Date(day) : new Date()
  if (!day) base.setMinutes(0, 0, 0)
  const start = new Date(base)
  if (!day) start.setHours(start.getHours() + 1)
  else start.setHours(9, 0, 0, 0)
  const end = new Date(start.getTime() + 60 * 60 * 1000)
  return { id: '', title: '', allDay: false, start, end, location: '', notes: '' }
}

export default function Calendar({ initialQuery = '' } = {}) {
  const { openWindow } = useShell()
  const [now, setNow] = useState(() => new Date())
  const [month, setMonth] = useState(() => startOfDay(new Date()))
  const [view, setView] = useState('month') // 'month' | 'agenda'
  const [events, setEvents] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(null) // form object or null
  const [saving, setSaving] = useState(false)

  // The window read range covers the whole 6-week grid (month view) with a day of
  // padding either side so events overlapping the edges still show.
  const range = useMemo(() => {
    const from = new Date(gridStart(month).getTime() - DAY_MS)
    const to = new Date(from.getTime() + 44 * DAY_MS)
    return { from, to }
  }, [month])

  const load = useCallback(() => {
    setLoading(true)
    listEvents(range.from, range.to)
      .then((evs) => { setEvents(evs); setError('') })
      .catch((e) => { setError(e?.message || 'unavailable'); setEvents([]) })
      .finally(() => setLoading(false))
  }, [range.from, range.to])

  useEffect(() => { load() }, [load])
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 60 * 1000)
    return () => clearInterval(t)
  }, [])

  // Deep-link: ⌘K "New event" launches Calendar with ?action=new — honour it by
  // pre-opening the new-event editor (seeded at the next hour). Runs once on
  // mount; the query is already consumed by the builtin factory.
  useEffect(() => {
    if (!initialQuery) return
    let action = ''
    try { action = new URLSearchParams(initialQuery).get('action') || '' } catch { action = '' }
    if (action === 'new') setEditing(blankEvent(null))
  }, [initialQuery])

  // events grouped by their day key (local yyyy-mm-dd) for the month grid.
  const byDay = useMemo(() => {
    const m = new Map()
    for (const ev of events) {
      if (!ev._start) continue
      const key = toDateInput(ev._start)
      if (!m.has(key)) m.set(key, [])
      m.get(key).push(ev)
    }
    return m
  }, [events])

  const days = useMemo(() => {
    const start = gridStart(month)
    return Array.from({ length: 42 }, (_, i) => new Date(start.getTime() + i * DAY_MS))
  }, [month])

  // Agenda: upcoming events from now, grouped by day, soonest first.
  const agenda = useMemo(() => {
    const cutoff = startOfDay(now)
    const groups = []
    let cur = null
    for (const ev of events) {
      if (!ev._start || ev._start < cutoff) continue
      const key = toDateInput(ev._start)
      if (!cur || cur.key !== key) {
        cur = { key, date: startOfDay(ev._start), items: [] }
        groups.push(cur)
      }
      cur.items.push(ev)
    }
    return groups
  }, [events, now])

  const openEditorForDay = (day) => setEditing(blankEvent(day))
  const openEditorForEvent = (ev) => setEditing({
    id: ev.id,
    title: ev.title,
    allDay: ev.allDay,
    start: ev._start || new Date(),
    end: ev._end || (ev._start ? new Date(ev._start.getTime() + 60 * 60 * 1000) : new Date()),
    location: ev.location,
    notes: ev.notes,
  })

  const save = async () => {
    if (!editing) return
    setSaving(true)
    const payload = {
      title: editing.title.trim() || '(untitled)',
      allDay: editing.allDay,
      location: editing.location,
      notes: editing.notes,
      start: (editing.allDay ? startOfDay(editing.start) : editing.start).toISOString(),
      end: (editing.allDay ? new Date(startOfDay(editing.end).getTime() + DAY_MS) : editing.end).toISOString(),
    }
    try {
      if (editing.id) await updateEvent(editing.id, payload)
      else await createEvent(payload)
      setEditing(null)
      load()
    } catch (e) {
      setError(e?.message || 'save failed')
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!editing?.id) { setEditing(null); return }
    setSaving(true)
    try {
      await deleteEvent(editing.id)
      setEditing(null)
      load()
    } catch (e) {
      setError(e?.message || 'delete failed')
    } finally {
      setSaving(false)
    }
  }

  const connectMail = () => {
    const app = getAppById('lilmail')
    if (app) launchApp(app, { openWindow })
  }

  const unavailable = !!error && events.length === 0

  return (
    <div className="h-full flex flex-col bg-neutral-950 text-neutral-100 select-none" data-calendar-app>
      {/* Toolbar — wraps on narrow screens; larger touch targets on mobile */}
      <div className="flex items-center flex-wrap gap-2 gap-y-2 px-3 sm:px-4 py-2.5 border-b border-neutral-800/70 shrink-0 bg-neutral-950/80 backdrop-blur-sm">
        <button
          type="button"
          onClick={() => setMonth(startOfDay(new Date()))}
          className="text-[12px] font-mono px-3 py-2 sm:py-1 rounded-md border border-neutral-700/80 hover:bg-neutral-800/60 hover:border-neutral-600 transition-colors focus-primary"
        >
          Today
        </button>
        <div className="flex items-center gap-0.5">
          <button type="button" aria-label="Previous month" onClick={() => setMonth(addMonths(month, -1))}
            className="w-11 h-11 sm:w-7 sm:h-7 text-lg sm:text-base grid place-items-center rounded-md text-neutral-400 hover:text-neutral-100 hover:bg-neutral-800/60 transition-colors focus-primary">‹</button>
          <button type="button" aria-label="Next month" onClick={() => setMonth(addMonths(month, 1))}
            className="w-11 h-11 sm:w-7 sm:h-7 text-lg sm:text-base grid place-items-center rounded-md text-neutral-400 hover:text-neutral-100 hover:bg-neutral-800/60 transition-colors focus-primary">›</button>
        </div>
        <div className="text-[15px] font-semibold min-w-0 truncate tracking-tight" data-calendar-title>
          {month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}
        </div>
        <span className="flex items-center gap-1 text-[9px] font-mono uppercase tracking-wider text-neutral-500 ml-1"
          title={unavailable ? 'Calendar unavailable' : loading ? 'Loading' : 'Calendar is live'}>
          {!unavailable && !loading && (
            <><span className="inline-block w-1.5 h-1.5 rounded-full animate-pulse" style={{ background: 'var(--status-success)' }} />live</>
          )}
        </span>

        <div className="ml-auto flex items-center gap-2">
          <div className="flex rounded-md border border-neutral-700/80 overflow-hidden text-[11px] font-mono p-0.5 gap-0.5">
            <button type="button" onClick={() => setView('month')}
              className={`px-3 py-1.5 sm:py-1 rounded transition-colors ${view === 'month' ? 'text-white shadow-sm' : 'text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800/40'}`}
              style={view === 'month' ? { background: 'var(--accent)' } : undefined}>Month</button>
            <button type="button" onClick={() => setView('agenda')}
              className={`px-3 py-1.5 sm:py-1 rounded transition-colors ${view === 'agenda' ? 'text-white shadow-sm' : 'text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800/40'}`}
              style={view === 'agenda' ? { background: 'var(--accent)' } : undefined}>Agenda</button>
          </div>
          <button
            type="button"
            onClick={() => openEditorForDay(view === 'month' ? month : now)}
            aria-label="New event"
            className="text-[12px] px-3 py-2 sm:py-1.5 rounded-md text-white font-medium transition-all hover:brightness-110 active:scale-[0.97] focus-primary whitespace-nowrap shadow-sm"
            style={{ background: 'var(--accent)' }}
          >
            + New event
          </button>
        </div>
      </div>

      {unavailable ? (
        <div className="flex-1 grid place-items-center text-center px-6 animate-[fadeIn_0.2s_ease-out]">
          <div className="max-w-xs">
            <div className="w-14 h-14 mx-auto mb-4 grid place-items-center rounded-2xl border border-neutral-800"
              style={{ background: 'var(--accent-soft)' }}>
              <svg viewBox="0 0 24 24" className="w-7 h-7" fill="none" stroke="var(--accent)" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="4.5" width="18" height="16" rx="2.5"/><path d="M3 9h18M8 2.5v4M16 2.5v4"/></svg>
            </div>
            <div className="text-neutral-200 text-sm font-medium">Calendar unavailable.</div>
            <div className="text-neutral-500 text-[12px] mt-1.5 leading-relaxed">
              Connect a mail account (Gmail, Outlook, or IMAP/CalDAV) to see and manage your events.
            </div>
            <button type="button" onClick={connectMail}
              className="mt-4 text-[12px] font-mono px-4 py-2 rounded-md border border-neutral-700 hover:bg-neutral-800/60 hover:border-neutral-600 transition-colors focus-primary">
              Connect Mail →
            </button>
          </div>
        </div>
      ) : view === 'month' ? (
        <MonthGrid days={days} month={month} now={now} byDay={byDay}
          onDay={openEditorForDay} onEvent={openEditorForEvent} />
      ) : (
        <AgendaList groups={agenda} now={now} onEvent={openEditorForEvent} loading={loading} />
      )}

      {editing && (
        <EventEditor
          form={editing}
          setForm={setEditing}
          onSave={save}
          onDelete={remove}
          onCancel={() => setEditing(null)}
          saving={saving}
        />
      )}
    </div>
  )
}

function MonthGrid({ days, month, now, byDay, onDay, onEvent }) {
  return (
    <div className="flex-1 flex flex-col min-h-0 animate-[fadeIn_0.18s_ease-out]">
      <div className="grid grid-cols-7 border-b border-neutral-800/70 shrink-0">
        {WEEKDAYS.map((w, i) => (
          <div key={w} className={`px-2 py-1.5 text-[10px] font-mono uppercase tracking-wider text-center ${i === 0 || i === 6 ? 'text-neutral-600' : 'text-neutral-500'}`}>{w}</div>
        ))}
      </div>
      <div className="grid grid-cols-7 grid-rows-6 flex-1 min-h-0">
        {days.map((day) => {
          const inMonth = day.getMonth() === month.getMonth()
          const isToday = isSameDay(day, now)
          const weekend = day.getDay() === 0 || day.getDay() === 6
          const items = byDay.get(toDateInput(day)) || []
          return (
            <button
              key={day.toISOString()}
              type="button"
              onClick={() => onDay(day)}
              aria-label={`Add event on ${day.toLocaleDateString()}`}
              className={`group relative text-left border-r border-b border-neutral-800/50 p-1 overflow-hidden flex flex-col gap-0.5 focus-primary transition-colors ${inMonth ? (weekend ? 'bg-neutral-900/20' : '') : 'bg-neutral-900/50'} ${isToday ? 'bg-[var(--accent-soft)]' : 'hover:bg-neutral-800/30'}`}
            >
              <div className="flex items-center justify-between">
                <span className={`text-[11px] font-mono grid place-items-center min-w-5 h-5 px-1 rounded-full transition-colors ${isToday ? 'text-white font-semibold' : inMonth ? 'text-neutral-300' : 'text-neutral-600'}`}
                  style={isToday ? { background: 'var(--accent)' } : undefined}>
                  {day.getDate()}
                </span>
                <span aria-hidden className="text-[13px] leading-none text-neutral-500 opacity-0 group-hover:opacity-100 transition-opacity pr-0.5">+</span>
              </div>
              <div className="flex flex-col gap-0.5 overflow-hidden">
                {items.slice(0, 3).map((ev) => (
                  <span
                    key={ev.id || ev.start}
                    role="button"
                    tabIndex={0}
                    onClick={(e) => { e.stopPropagation(); onEvent(ev) }}
                    onKeyDown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); onEvent(ev) } }}
                    className="flex items-center gap-1 text-[10px] leading-tight px-1 py-0.5 rounded truncate cursor-pointer transition-all hover:brightness-110"
                    style={{ background: 'var(--accent-soft)', color: 'var(--accent)' }}
                    title={ev.title}
                  >
                    <span className="w-1 h-1 rounded-full shrink-0" style={{ background: 'var(--accent)' }} />
                    {!ev.allDay && ev._start ? <span className="opacity-70 font-mono shrink-0">{fmtTime(ev._start)}</span> : null}
                    <span className="truncate">{ev.title || '(untitled)'}</span>
                  </span>
                ))}
                {items.length > 3 && (
                  <span className="text-[9px] text-neutral-500 px-1 font-mono">+{items.length - 3} more</span>
                )}
              </div>
            </button>
          )
        })}
      </div>
    </div>
  )
}

function AgendaList({ groups, now, onEvent, loading }) {
  if (loading && groups.length === 0) {
    return (
      <div className="flex-1 grid place-items-center text-neutral-500 text-sm gap-2">
        <span className="w-4 h-4 spinner" /> Loading…
      </div>
    )
  }
  if (groups.length === 0) {
    return (
      <div className="flex-1 grid place-items-center text-center px-6 animate-[fadeIn_0.2s_ease-out]">
        <div className="max-w-xs">
          <div className="w-12 h-12 mx-auto mb-3 grid place-items-center rounded-2xl border border-neutral-800 text-neutral-600">
            <svg viewBox="0 0 24 24" className="w-6 h-6" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="4.5" width="18" height="16" rx="2.5"/><path d="M3 9h18M8 2.5v4M16 2.5v4"/></svg>
          </div>
          <div className="text-neutral-400 text-sm">Nothing on your calendar ahead.</div>
          <div className="text-neutral-600 text-[12px] mt-1">Your upcoming events will appear here.</div>
        </div>
      </div>
    )
  }
  return (
    <div className="flex-1 overflow-y-auto animate-[fadeIn_0.18s_ease-out]">
      {groups.map((g) => {
        const isToday = isSameDay(g.date, now)
        return (
        <div key={g.key}>
          <div className="sticky top-0 z-10 flex items-center gap-2 bg-neutral-950/95 backdrop-blur px-4 py-1.5 text-[11px] font-mono uppercase tracking-wider border-b border-neutral-800/50"
            style={isToday ? { color: 'var(--accent)' } : undefined}>
            {isToday && <span className="w-1.5 h-1.5 rounded-full" style={{ background: 'var(--accent)' }} />}
            <span className={isToday ? '' : 'text-neutral-500'}>{fmtDayLabel(g.date, now)}</span>
          </div>
          <ul>
            {g.items.map((ev) => (
              <li key={ev.id || ev.start}>
                <button type="button" onClick={() => onEvent(ev)}
                  className="w-full flex items-stretch gap-3 px-4 py-2.5 text-left hover:bg-neutral-800/40 transition-colors focus-primary border-b border-neutral-900 group">
                  <div className="w-16 shrink-0 text-right text-[12px] font-mono text-neutral-400 pt-0.5">
                    {ev.allDay ? 'All day' : fmtTime(ev._start) || '—'}
                  </div>
                  <div className="w-0.5 self-stretch rounded-full shrink-0 transition-colors" style={{ background: 'var(--accent)', opacity: 0.55 }} />
                  <div className="min-w-0 flex-1">
                    <div className="text-[13px] text-neutral-100 truncate group-hover:text-white transition-colors">{ev.title || '(untitled)'}</div>
                    {ev.location && (
                      <div className="flex items-center gap-1 text-[11px] text-neutral-500 truncate mt-0.5">
                        <svg viewBox="0 0 16 16" className="w-3 h-3 shrink-0" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M8 1.5c-2.5 0-4.5 2-4.5 4.5 0 3 4.5 8 4.5 8s4.5-5 4.5-8c0-2.5-2-4.5-4.5-4.5z"/><circle cx="8" cy="6" r="1.5"/></svg>
                        <span className="truncate">{ev.location}</span>
                      </div>
                    )}
                  </div>
                </button>
              </li>
            ))}
          </ul>
        </div>
        )
      })}
    </div>
  )
}

function EventEditor({ form, setForm, onSave, onDelete, onCancel, saving }) {
  const set = (patch) => setForm((f) => ({ ...f, ...patch }))
  const onStart = (v) => {
    const d = parseDate(v)
    if (d) set({ start: d, end: form.end < d ? new Date(d.getTime() + 60 * 60 * 1000) : form.end })
  }
  const onEnd = (v) => { const d = parseDate(v); if (d) set({ end: d }) }
  return (
    <div className="absolute inset-0 z-40 grid place-items-center bg-black/50 backdrop-blur-sm p-4 animate-[fadeIn_0.15s_ease-out]" onClick={onCancel} data-event-editor>
      <div className="w-full max-w-sm rounded-2xl border border-neutral-800 bg-neutral-900 shadow-2xl shadow-black/50 overflow-hidden anim-sheet-up"
        onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-2 px-4 py-3 border-b border-neutral-800 text-[13px] font-semibold">
          <span className="w-1.5 h-5 rounded-full" style={{ background: 'var(--accent)' }} />
          {form.id ? 'Edit event' : 'New event'}
        </div>
        <div className="p-4 flex flex-col gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Title</span>
            <input
              autoFocus
              value={form.title}
              onChange={(e) => set({ title: e.target.value })}
              placeholder="Event title"
              className="bg-neutral-800/60 border border-neutral-700/80 transition-colors focus:border-neutral-600 rounded-md px-2.5 py-1.5 text-[13px] focus-primary"
            />
          </label>

          <label className="flex items-center gap-2 text-[12px] text-neutral-300">
            <input type="checkbox" checked={form.allDay} onChange={(e) => set({ allDay: e.target.checked })} />
            All day
          </label>

          <div className="grid grid-cols-2 gap-2">
            <label className="flex flex-col gap-1">
              <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Starts</span>
              <input
                type={form.allDay ? 'date' : 'datetime-local'}
                value={form.allDay ? toDateInput(form.start) : toLocalInput(form.start)}
                onChange={(e) => onStart(e.target.value)}
                aria-label="Start"
                className="bg-neutral-800/60 border border-neutral-700/80 transition-colors focus:border-neutral-600 rounded-md px-2 py-1.5 text-[12px] focus-primary"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Ends</span>
              <input
                type={form.allDay ? 'date' : 'datetime-local'}
                value={form.allDay ? toDateInput(form.end) : toLocalInput(form.end)}
                onChange={(e) => onEnd(e.target.value)}
                aria-label="End"
                className="bg-neutral-800/60 border border-neutral-700/80 transition-colors focus:border-neutral-600 rounded-md px-2 py-1.5 text-[12px] focus-primary"
              />
            </label>
          </div>

          <label className="flex flex-col gap-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Location</span>
            <input
              value={form.location}
              onChange={(e) => set({ location: e.target.value })}
              placeholder="Add a location"
              className="bg-neutral-800/60 border border-neutral-700/80 transition-colors focus:border-neutral-600 rounded-md px-2.5 py-1.5 text-[13px] focus-primary"
            />
          </label>

          <label className="flex flex-col gap-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Notes</span>
            <textarea
              value={form.notes}
              onChange={(e) => set({ notes: e.target.value })}
              rows={2}
              placeholder="Add notes"
              className="bg-neutral-800/60 border border-neutral-700/80 transition-colors focus:border-neutral-600 rounded-md px-2.5 py-1.5 text-[13px] resize-none focus-primary"
            />
          </label>
        </div>
        <div className="px-4 py-3 border-t border-neutral-800 flex items-center gap-2">
          {form.id && (
            <button type="button" onClick={onDelete} disabled={saving}
              className="text-[12px] px-2.5 py-1.5 rounded-md text-danger hover:bg-danger-soft focus-primary disabled:opacity-50">
              Delete
            </button>
          )}
          <button type="button" onClick={onCancel} disabled={saving}
            className="ml-auto text-[12px] px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary disabled:opacity-50">
            Cancel
          </button>
          <button type="button" onClick={onSave} disabled={saving}
            className="text-[12px] px-3 py-1.5 rounded-md text-white font-medium transition-all hover:brightness-110 active:scale-[0.97] focus-primary disabled:opacity-50 shadow-sm"
            style={{ background: 'var(--accent)' }}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}
