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

export default function Calendar() {
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
      {/* Toolbar */}
      <div className="flex items-center gap-2 px-4 py-2.5 border-b border-neutral-800/70 shrink-0">
        <button
          type="button"
          onClick={() => setMonth(startOfDay(new Date()))}
          className="text-[12px] font-mono px-2.5 py-1 rounded-md border border-neutral-700 hover:bg-neutral-800/60 transition-colors focus-primary"
        >
          Today
        </button>
        <div className="flex items-center gap-0.5">
          <button type="button" aria-label="Previous month" onClick={() => setMonth(addMonths(month, -1))}
            className="w-7 h-7 grid place-items-center rounded-md hover:bg-neutral-800/60 focus-primary">‹</button>
          <button type="button" aria-label="Next month" onClick={() => setMonth(addMonths(month, 1))}
            className="w-7 h-7 grid place-items-center rounded-md hover:bg-neutral-800/60 focus-primary">›</button>
        </div>
        <div className="text-[15px] font-semibold min-w-0 truncate" data-calendar-title>
          {month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}
        </div>
        <span className="flex items-center gap-1 text-[9px] font-mono uppercase tracking-wider text-neutral-500 ml-1"
          title={unavailable ? 'Calendar unavailable' : loading ? 'Loading' : 'Calendar is live'}>
          {!unavailable && !loading && (
            <><span className="inline-block w-1.5 h-1.5 rounded-full" style={{ background: 'var(--status-success)' }} />live</>
          )}
        </span>

        <div className="ml-auto flex items-center gap-2">
          <div className="flex rounded-md border border-neutral-700 overflow-hidden text-[11px] font-mono">
            <button type="button" onClick={() => setView('month')}
              className={`px-2.5 py-1 ${view === 'month' ? 'bg-neutral-800 text-neutral-100' : 'text-neutral-400 hover:bg-neutral-800/40'}`}>Month</button>
            <button type="button" onClick={() => setView('agenda')}
              className={`px-2.5 py-1 ${view === 'agenda' ? 'bg-neutral-800 text-neutral-100' : 'text-neutral-400 hover:bg-neutral-800/40'}`}>Agenda</button>
          </div>
          <button
            type="button"
            onClick={() => openEditorForDay(view === 'month' ? month : now)}
            className="text-[12px] px-2.5 py-1 rounded-md text-white transition-colors focus-primary"
            style={{ background: 'var(--accent)' }}
          >
            + New event
          </button>
        </div>
      </div>

      {unavailable ? (
        <div className="flex-1 grid place-items-center text-center px-6">
          <div>
            <div className="text-neutral-300 text-sm">Calendar unavailable.</div>
            <div className="text-neutral-500 text-[12px] mt-1 max-w-xs mx-auto">
              Connect a mail account (Gmail, Outlook, or IMAP/CalDAV) to see and manage your events.
            </div>
            <button type="button" onClick={connectMail}
              className="mt-3 text-[12px] font-mono px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary">
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
    <div className="flex-1 flex flex-col min-h-0">
      <div className="grid grid-cols-7 border-b border-neutral-800/70 shrink-0">
        {WEEKDAYS.map((w) => (
          <div key={w} className="px-2 py-1.5 text-[10px] font-mono uppercase tracking-wider text-neutral-500 text-center">{w}</div>
        ))}
      </div>
      <div className="grid grid-cols-7 grid-rows-6 flex-1 min-h-0">
        {days.map((day) => {
          const inMonth = day.getMonth() === month.getMonth()
          const isToday = isSameDay(day, now)
          const items = byDay.get(toDateInput(day)) || []
          return (
            <button
              key={day.toISOString()}
              type="button"
              onClick={() => onDay(day)}
              aria-label={`Add event on ${day.toLocaleDateString()}`}
              className={`text-left border-r border-b border-neutral-800/50 p-1 overflow-hidden flex flex-col gap-0.5 focus-primary hover:bg-neutral-800/30 transition-colors ${inMonth ? '' : 'bg-neutral-900/40'}`}
            >
              <div className="flex items-center justify-between">
                <span className={`text-[11px] font-mono grid place-items-center w-5 h-5 rounded-full ${isToday ? 'text-white' : inMonth ? 'text-neutral-300' : 'text-neutral-600'}`}
                  style={isToday ? { background: 'var(--accent)' } : undefined}>
                  {day.getDate()}
                </span>
              </div>
              <div className="flex flex-col gap-0.5 overflow-hidden">
                {items.slice(0, 3).map((ev) => (
                  <span
                    key={ev.id || ev.start}
                    role="button"
                    tabIndex={0}
                    onClick={(e) => { e.stopPropagation(); onEvent(ev) }}
                    onKeyDown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); onEvent(ev) } }}
                    className="text-[10px] leading-tight px-1 py-0.5 rounded truncate cursor-pointer"
                    style={{ background: 'var(--accent-soft)', color: 'var(--accent)' }}
                    title={ev.title}
                  >
                    {!ev.allDay && ev._start ? <span className="opacity-70 mr-1">{fmtTime(ev._start)}</span> : null}
                    {ev.title || '(untitled)'}
                  </span>
                ))}
                {items.length > 3 && (
                  <span className="text-[9px] text-neutral-500 px-1">+{items.length - 3} more</span>
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
    return <div className="flex-1 grid place-items-center text-neutral-500 text-sm">Loading…</div>
  }
  if (groups.length === 0) {
    return <div className="flex-1 grid place-items-center text-neutral-500 text-sm">Nothing on your calendar ahead.</div>
  }
  return (
    <div className="flex-1 overflow-y-auto">
      {groups.map((g) => (
        <div key={g.key}>
          <div className="sticky top-0 bg-neutral-950/95 backdrop-blur px-4 py-1.5 text-[11px] font-mono uppercase tracking-wider text-neutral-500 border-b border-neutral-800/50">
            {fmtDayLabel(g.date, now)}
          </div>
          <ul>
            {g.items.map((ev) => (
              <li key={ev.id || ev.start}>
                <button type="button" onClick={() => onEvent(ev)}
                  className="w-full flex items-start gap-3 px-4 py-2 text-left hover:bg-neutral-800/40 transition-colors focus-primary border-b border-neutral-900">
                  <div className="w-16 shrink-0 text-right text-[12px] font-mono text-neutral-300 pt-0.5">
                    {ev.allDay ? 'All day' : fmtTime(ev._start) || '—'}
                  </div>
                  <div className="w-px self-stretch bg-neutral-800" />
                  <div className="min-w-0">
                    <div className="text-[13px] text-neutral-100 truncate">{ev.title || '(untitled)'}</div>
                    {ev.location && <div className="text-[11px] text-neutral-500 truncate">{ev.location}</div>}
                  </div>
                </button>
              </li>
            ))}
          </ul>
        </div>
      ))}
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
    <div className="absolute inset-0 z-40 grid place-items-center bg-black/50 p-4" onClick={onCancel} data-event-editor>
      <div className="w-full max-w-sm rounded-2xl border border-neutral-800 bg-neutral-900 shadow-2xl shadow-black/50 overflow-hidden"
        onClick={(e) => e.stopPropagation()}>
        <div className="px-4 py-3 border-b border-neutral-800 text-[13px] font-semibold">
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
              className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[13px] focus-primary"
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
                className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2 py-1.5 text-[12px] focus-primary"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Ends</span>
              <input
                type={form.allDay ? 'date' : 'datetime-local'}
                value={form.allDay ? toDateInput(form.end) : toLocalInput(form.end)}
                onChange={(e) => onEnd(e.target.value)}
                aria-label="End"
                className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2 py-1.5 text-[12px] focus-primary"
              />
            </label>
          </div>

          <label className="flex flex-col gap-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Location</span>
            <input
              value={form.location}
              onChange={(e) => set({ location: e.target.value })}
              placeholder="Add a location"
              className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[13px] focus-primary"
            />
          </label>

          <label className="flex flex-col gap-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-500">Notes</span>
            <textarea
              value={form.notes}
              onChange={(e) => set({ notes: e.target.value })}
              rows={2}
              placeholder="Add notes"
              className="bg-neutral-800/60 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[13px] resize-none focus-primary"
            />
          </label>
        </div>
        <div className="px-4 py-3 border-t border-neutral-800 flex items-center gap-2">
          {form.id && (
            <button type="button" onClick={onDelete} disabled={saving}
              className="text-[12px] px-2.5 py-1.5 rounded-md text-red-300 hover:bg-red-500/10 focus-primary disabled:opacity-50">
              Delete
            </button>
          )}
          <button type="button" onClick={onCancel} disabled={saving}
            className="ml-auto text-[12px] px-3 py-1.5 rounded-md border border-neutral-700 hover:bg-neutral-800/60 focus-primary disabled:opacity-50">
            Cancel
          </button>
          <button type="button" onClick={onSave} disabled={saving}
            className="text-[12px] px-3 py-1.5 rounded-md text-white focus-primary disabled:opacity-50"
            style={{ background: 'var(--accent)' }}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}
