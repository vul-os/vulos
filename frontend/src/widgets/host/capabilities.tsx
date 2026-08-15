// capabilities.tsx — the host side of the permissioned data seams.
//
// Every seam a widget can read is opened ONCE, by the rail, and shared. That is
// the difference between a widget system and a pile of components: five widgets
// that each called `useTelemetry()` would open five WebSockets to the box, and
// five widgets that each fetched the agenda would issue five identical requests
// on every mount. The rail opens one of each, only when at least one mounted
// widget was granted the matching permission, and closes it when the last such
// widget goes away.
//
// The sources are COMPONENTS rather than hooks because that mount/unmount is the
// mechanism: a hook cannot be called conditionally, but a component can be
// rendered conditionally, so `{needTelemetry && <TelemetrySource …/>}` is what
// makes "no widget wants telemetry ⇒ no socket" true rather than aspirational.
//
// The wire-format narrowing lives in narrow.ts so it stays pure and testable.

import { useEffect, useSyncExternalStore } from 'react'
import { useTelemetry } from '../../core/useTelemetry'
import { listEvents } from '../../builtin/calendar/calendarApi'
import { subscribe, getItems, getUnreadCount } from '../../core/notificationStore'
import { narrowEvents, narrowNotifications, narrowTelemetry } from './narrow'
import type { WidgetCalendar, WidgetNotifications, WidgetTelemetry } from '../types'

export function TelemetrySource({ onChange }: { onChange: (t: WidgetTelemetry) => void }) {
  const { stats, connected } = useTelemetry()
  // `onChange` is a useState setter in the rail, so its identity is stable and
  // depending on it costs nothing. Depending on it rather than stashing it in a
  // ref is the correct form: a ref written during render is a render-phase
  // mutation, and React may discard that render.
  useEffect(() => { onChange(narrowTelemetry(stats, connected)) }, [stats, connected, onChange])
  return null
}

export function NotificationSource({ onChange }: { onChange: (n: WidgetNotifications) => void }) {
  const items = useSyncExternalStore<unknown>(subscribe, getItems as () => unknown)
  const unread = useSyncExternalStore<number>(subscribe, getUnreadCount as () => number)
  useEffect(() => { onChange(narrowNotifications(items, unread)) }, [items, unread, onChange])
  return null
}

const DAY_MS = 86_400_000
const AGENDA_REFRESH_MS = 5 * 60 * 1000

export function CalendarSource({ onChange }: { onChange: (c: WidgetCalendar) => void }) {
  useEffect(() => {
    let alive = true
    const load = () => {
      // Today → +8 days, so "the week ahead" is covered with a day of slack.
      const from = new Date(); from.setHours(0, 0, 0, 0)
      const to = new Date(from.getTime() + 8 * DAY_MS)
      listEvents(from, to)
        .then((evs: unknown) => { if (alive) onChange({ events: narrowEvents(evs), error: false }) })
        // An unreachable calendar backend is `error: true` with events null, NOT
        // an empty agenda. "You have nothing on" and "we could not ask" are
        // different statements and the widget must be able to tell them apart.
        .catch(() => { if (alive) onChange({ events: null, error: true }) })
    }
    load()
    const t = setInterval(load, AGENDA_REFRESH_MS)
    return () => { alive = false; clearInterval(t) }
  }, [onChange])
  return null
}
