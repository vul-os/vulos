// agenda.tsx — what's next on the calendar, and the week behind it.
//
// This is the widget that replaced src/shell/CalendarWidget.tsx in the rail. It
// keeps that component's contract deliberately and exactly — the
// `data-calendar-widget` marker, the live/stale dot, "next up" collapsed, the
// week on expand, "Open Calendar →", and the Connect-Mail path when the backend
// is unreachable — because those are a shipped, gated behaviour
// (e2e/calendar-widget.e2e.ts) and a rewrite is not a licence to drop them.
//
// It renders its own <section> rather than WidgetFrame for one reason: the
// `data-calendar-widget` marker the existing suite locates it by. A widget IS
// free to render its own markup — the primitives are a convenience, not a
// requirement — and this is the one builtin that takes that option, so the
// escape hatch is exercised rather than merely claimed.
//
// THREE STATES, AND TELLING THEM APART IS THE JOB:
//   loading      events === null, error === false
//   unreachable  events === null, error === true    → say so, offer Mail
//   empty        events === []                      → "nothing on", which is REAL
// Collapsing the last two into one blank list is the defect this widget exists
// to avoid: "you have nothing on" and "we could not ask" look identical on
// screen and mean opposite things.
import { useState } from 'react'
import {
  defineWidget, registerWidget,
  WidgetLabel,
  type WidgetContext, type WidgetEvent,
} from '../index'
import { upcomingFrom } from './logic'

function isSameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

function relDay(d: Date, now: Date): string {
  if (isSameDay(d, now)) return 'Today'
  const tm = new Date(now)
  tm.setDate(tm.getDate() + 1)
  if (isSameDay(d, tm)) return 'Tomorrow'
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
}

function when(ev: WidgetEvent): string {
  if (ev.allDay) return 'All day'
  return ev.start ? ev.start.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : '—'
}

function EventRow({ ev, now, onOpen, dim }: { ev: WidgetEvent; now: Date; onOpen: () => void; dim?: boolean }) {
  return (
    <button type="button" onClick={onOpen} className="vwidget-row focus-primary" aria-label={`Open ${ev.title || 'event'} in Calendar`}>
      {/* min-width + nowrap: at this size a 12-hour time ("09:00 AM") is ~58px
          and wrapped inside a 56px box, putting the hour and the meridiem on
          separate lines with the relative day stranded underneath. */}
      <span className="shrink-0 text-right whitespace-nowrap" style={{ minWidth: '3.5rem' }}>
        <span className="block"><WidgetLabel tone={dim ? 'secondary' : 'accent'} mono>{when(ev)}</WidgetLabel></span>
        <span className="block"><WidgetLabel tone="faint" mono>{ev.start ? relDay(ev.start, now) : ''}</WidgetLabel></span>
      </span>
      <span className="w-px self-stretch" style={{ background: 'color-mix(in srgb, var(--border-strong) 55%, transparent)' }} />
      <span className="min-w-0 flex-1">
        <span className="block truncate"><WidgetLabel tone="primary">{ev.title || '(untitled)'}</WidgetLabel></span>
        {ev.location && <span className="block truncate"><WidgetLabel tone="faint">{ev.location}</WidgetLabel></span>}
      </span>
    </button>
  )
}

export default function AgendaWidget(ctx: WidgetContext) {
  const [expanded, setExpanded] = useState(false)
  const cal = ctx.calendar
  const openCalendar = () => ctx.openApp?.('vulos-calendar')
  const openMail = () => ctx.openApp?.('lilmail')

  const loading = !!cal && cal.events === null && !cal.error
  const unreachable = !!cal && cal.error
  const events = cal?.events ?? []
  const upcoming = upcomingFrom(events, ctx.now)
  const next = upcoming[0] ?? null
  // "live" = the last read succeeded, even if the agenda is empty. That is a
  // statement about the SEAM, not about the events, which is why an empty
  // calendar still reads live.
  const fresh = !!cal && !cal.error && cal.events !== null

  return (
    <section className="vwidget-card" data-calendar-widget aria-label="Agenda">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        aria-label={expanded ? 'Collapse calendar' : 'Expand calendar'}
        className="w-full flex items-center justify-between gap-2 px-3.5 py-2.5 text-left focus-primary vwidget-row"
        style={{ margin: 0, borderRadius: 0 }}
      >
        <span className="min-w-0">
          <span className="block"><WidgetLabel tone="faint" mono>{ctx.now.toLocaleDateString(undefined, { weekday: 'long' })}</WidgetLabel></span>
          <span className="block text-[15px] font-semibold leading-tight" style={{ color: 'var(--text-primary)' }}>
            {ctx.now.toLocaleDateString(undefined, { month: 'long', day: 'numeric' })}
          </span>
        </span>
        {!loading && !unreachable && (
          <span className="flex items-center gap-1 shrink-0">
            <span
              className="inline-block w-1.5 h-1.5 rounded-full"
              style={{ background: fresh ? 'var(--status-success)' : 'var(--status-danger)' }}
              aria-hidden="true"
            />
            <WidgetLabel tone="faint" mono>{fresh ? 'live' : 'stale'}</WidgetLabel>
          </span>
        )}
      </button>

      <div className="px-3.5 pb-2.5">
        {!cal ? (
          <WidgetLabel tone="faint">Allow “Read your agenda” in this widget’s settings.</WidgetLabel>
        ) : unreachable ? (
          <span className="block">
            <span className="block"><WidgetLabel tone="faint">Calendar unavailable.</WidgetLabel></span>
            {ctx.openApp && (
              <button type="button" onClick={openMail} className="vwidget-link focus-primary mt-1">Connect Mail →</button>
            )}
          </span>
        ) : loading ? (
          <div className="h-8 rounded-lg animate-pulse" style={{ background: 'color-mix(in srgb, var(--bg-hover) 60%, transparent)' }} />
        ) : next ? (
          <EventRow ev={next} now={ctx.now} onOpen={openCalendar} />
        ) : (
          <WidgetLabel tone="faint">Nothing on your calendar.</WidgetLabel>
        )}
      </div>

      {expanded && (
        <div className="vwidget-scroll" style={{ borderTop: '1px solid color-mix(in srgb, var(--border-strong) 55%, transparent)' }}>
          {unreachable ? (
            <div className="px-3.5 py-3"><WidgetLabel tone="faint">Connect Mail to see your agenda here.</WidgetLabel></div>
          ) : upcoming.length <= 1 ? (
            <div className="px-3.5 py-3">
              <WidgetLabel tone="faint">
                {upcoming.length === 0 ? 'Nothing on your calendar for the week ahead.' : 'No further events this week.'}
              </WidgetLabel>
            </div>
          ) : (
            <div className="px-3.5 py-1">
              {upcoming.slice(1).map((ev) => (
                <EventRow key={ev.id} ev={ev} now={ctx.now} onOpen={openCalendar} dim />
              ))}
            </div>
          )}
          <div className="px-3.5 py-2" style={{ borderTop: '1px solid color-mix(in srgb, var(--border-strong) 55%, transparent)' }}>
            <button type="button" onClick={openCalendar} className="vwidget-link focus-primary">Open Calendar →</button>
          </div>
        </div>
      )}
    </section>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.agenda',
    name: 'Agenda',
    description: 'What’s next on your calendar, and the week behind it.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['medium', 'large'],
    tick: 'minute',
    permissions: ['calendar', 'launch'],
  },
  // Rendered as a COMPONENT (<AgendaWidget/>), never called as a function: it
  // holds hooks, and calling it would splice its hook order into its caller's.
  render: (ctx) => <AgendaWidget {...ctx} />,
}))
