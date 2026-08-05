// calendarApi.ts — the standalone Calendar surface's read/write seam over the
// box's PIM proxy (/api/pim/calendar/*), which brokers to lilmail's /v1 calendar
// (CalDAV, plus any OAuth-connected Google/Outlook calendars lilmail aggregates).
//
// The browser never sees mail credentials: it calls the box with its own session
// cookie, and the box injects the brokered creds (see backend routes_pim.go).
//
// Everything here is defensive: a malformed event or a non-JSON response can
// never throw into the render tree — reads degrade to an empty list with an
// error flag, so the Calendar shows an honest "unavailable" state, never a crash.

const BASE = '/api/pim/calendar'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// A wire field is honoured only when it actually is the expected (non-empty)
// string type — matches the original `a || b || ''` fallback chain for the
// realistic wire shape, while degrading a malformed field (e.g. a server bug
// returning a number) to the fallback instead of leaking an unexpected
// runtime type into the render tree.
function str(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

// HttpError — thrown by req() on a non-ok response. Carries `status` (nothing
// currently reads it; callers read `.message`) without resorting to
// `(err as any).status = …`.
export class HttpError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'HttpError'
    this.status = status
  }
}

// parseDate returns a valid Date or null (never throws on a bad ISO string).
export function parseDate(iso: string | null | undefined): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return isNaN(d.getTime()) ? null : d
}

// The shape the UI renders — every field is defaulted, never undefined, so
// consumers can read straight through without optional-chaining.
export interface CalendarEvent {
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

// normalizeEvent maps lilmail's /v1 event (tolerating title/summary and
// start/dtStart spellings) onto the shape the UI renders. An event with neither
// a title nor a start is dropped by the caller.
export function normalizeEvent(e: unknown): CalendarEvent | null {
  if (!isRecord(e)) return null
  const id = str(e.uid) || str(e.id)
  const title = str(e.title) || str(e.summary)
  const start = str(e.start) || str(e.dtStart)
  const end = str(e.end) || str(e.dtEnd)
  const _start = parseDate(start)
  if (!title && !_start) return null
  return {
    id,
    title,
    start,
    end,
    _start,
    _end: parseDate(end),
    location: str(e.location),
    notes: str(e.description) || str(e.notes),
    allDay: !!e.allDay,
  }
}

async function req(method: string, path: string, body?: unknown): Promise<unknown> {
  const headers: Record<string, string> = {}
  const opts: RequestInit = { method, credentials: 'include', headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(BASE + path, opts)
  if (!res.ok) {
    const txt = await res.text().catch(() => '')
    throw new HttpError(txt || `HTTP ${res.status}`, res.status)
  }
  // 204 (DELETE) and empty bodies decode to null rather than throwing.
  const text = await res.text()
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

// listEvents reads events overlapping [from, to] (Date objects). Returns a sorted
// array of normalized events. Tolerates {events:[...]} or a bare array.
export async function listEvents(from: Date | string, to: Date | string): Promise<CalendarEvent[]> {
  const q = new URLSearchParams()
  const fi = from instanceof Date ? from.toISOString() : String(from)
  const ti = to instanceof Date ? to.toISOString() : String(to)
  q.set('start', fi)
  q.set('from', fi)
  q.set('end', ti)
  q.set('to', ti)
  const data = await req('GET', '/events?' + q.toString())
  const raw: unknown[] = Array.isArray(data)
    ? data
    : isRecord(data) && Array.isArray(data.events)
      ? data.events
      : []
  return raw
    .map(normalizeEvent)
    .filter((ev): ev is CalendarEvent => ev !== null)
    .sort((a, b) => (a._start?.getTime() || 0) - (b._start?.getTime() || 0))
}

// EventFormInput — the shape the editor form (Calendar.tsx) hands to
// create/update: start/end are ISO strings by the time they reach here.
export interface EventFormInput {
  title?: string
  start?: string
  end?: string
  location?: string
  notes?: string
  allDay?: boolean
}

// eventPayload builds lilmail's create/update body from the editor form.
function eventPayload(ev: EventFormInput) {
  return {
    summary: ev.title || '',
    start: ev.start || '',
    end: ev.end || '',
    location: ev.location || '',
    description: ev.notes || '',
    allDay: !!ev.allDay,
  }
}

export async function createEvent(ev: EventFormInput): Promise<unknown> {
  return req('POST', '/events', eventPayload(ev))
}

export async function updateEvent(id: string, ev: EventFormInput): Promise<unknown> {
  return req('PUT', '/events/' + encodeURIComponent(id), eventPayload(ev))
}

export async function deleteEvent(id: string): Promise<unknown> {
  return req('DELETE', '/events/' + encodeURIComponent(id))
}
