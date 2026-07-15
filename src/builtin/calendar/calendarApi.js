// calendarApi.js — the standalone Calendar surface's read/write seam over the
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

// parseDate returns a valid Date or null (never throws on a bad ISO string).
export function parseDate(iso) {
  if (!iso) return null
  const d = new Date(iso)
  return isNaN(d.getTime()) ? null : d
}

// normalizeEvent maps lilmail's /v1 event (tolerating title/summary and
// start/dtStart spellings) onto the shape the UI renders. An event with neither
// a title nor a start is dropped by the caller.
export function normalizeEvent(e) {
  if (!e || typeof e !== 'object') return null
  const id = e.uid || e.id || ''
  const title = e.title || e.summary || ''
  const start = e.start || e.dtStart || ''
  const end = e.end || e.dtEnd || ''
  const _start = parseDate(start)
  if (!title && !_start) return null
  return {
    id,
    title,
    start,
    end,
    _start,
    _end: parseDate(end),
    location: e.location || '',
    notes: e.description || e.notes || '',
    allDay: !!e.allDay,
  }
}

async function req(method, path, body) {
  const opts = { method, credentials: 'include', headers: {} }
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(BASE + path, opts)
  if (!res.ok) {
    const txt = await res.text().catch(() => '')
    const err = new Error(txt || `HTTP ${res.status}`)
    err.status = res.status
    throw err
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
export async function listEvents(from, to) {
  const q = new URLSearchParams()
  const fi = from instanceof Date ? from.toISOString() : String(from)
  const ti = to instanceof Date ? to.toISOString() : String(to)
  q.set('start', fi)
  q.set('from', fi)
  q.set('end', ti)
  q.set('to', ti)
  const data = await req('GET', '/events?' + q.toString())
  const raw = Array.isArray(data) ? data : Array.isArray(data?.events) ? data.events : []
  return raw
    .map(normalizeEvent)
    .filter(Boolean)
    .sort((a, b) => (a._start?.getTime() || 0) - (b._start?.getTime() || 0))
}

// eventPayload builds lilmail's create/update body from the editor form.
function eventPayload(ev) {
  return {
    summary: ev.title || '',
    start: ev.start || '',
    end: ev.end || '',
    location: ev.location || '',
    description: ev.notes || '',
    allDay: !!ev.allDay,
  }
}

export async function createEvent(ev) {
  return req('POST', '/events', eventPayload(ev))
}

export async function updateEvent(id, ev) {
  return req('PUT', '/events/' + encodeURIComponent(id), eventPayload(ev))
}

export async function deleteEvent(id) {
  return req('DELETE', '/events/' + encodeURIComponent(id))
}
