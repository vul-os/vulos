// calendarApi.test.ts — the Calendar surface's /v1 seam (via /api/pim/calendar).
//
// Proves the read path is defensive (tolerates the {events:[]} envelope, a bare
// array, and both title/summary + start/dtStart spellings; drops junk events),
// and that the write path maps the editor form onto lilmail's create/update body.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { normalizeEvent, listEvents, createEvent, updateEvent, deleteEvent } from '../calendarApi'

function mockFetch(status: number, body?: unknown) {
  const text = typeof body === 'string' ? body : JSON.stringify(body ?? '')
  // Real Response objects (not a duck-typed stand-in): a non-null empty-string
  // body isn't a valid init for a no-content status like 204, so an empty
  // text falls back to `undefined` — matching the real absence of a body.
  return vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
    async () => new Response(text || undefined, { status }),
  )
}

beforeEach(() => { vi.stubGlobal('fetch', mockFetch(200, { events: [] })) })
afterEach(() => { vi.restoreAllMocks() })

describe('normalizeEvent', () => {
  it('tolerates title/summary and start/dtStart spellings', () => {
    const a = normalizeEvent({ uid: 'a', summary: 'Sync', dtStart: '2026-01-01T09:00:00Z' })
    if (!a) throw new Error('expected a normalized event')
    expect(a.title).toBe('Sync')
    const b = normalizeEvent({ id: 'b', title: 'Call', start: '2026-01-01T10:00:00Z' })
    if (!b) throw new Error('expected a normalized event')
    expect(b.id).toBe('b')
  })
  it('drops an event with neither title nor a parseable start', () => {
    expect(normalizeEvent({ uid: 'x', start: 'not-a-date' })).toBeNull()
    expect(normalizeEvent(null)).toBeNull()
  })
  it('parses dates into _start/_end without throwing on bad input', () => {
    const ev = normalizeEvent({ title: 'T', start: '2026-01-01T09:00:00Z', end: 'garbage' })
    if (!ev) throw new Error('expected a normalized event')
    expect(ev._start).toBeInstanceOf(Date)
    expect(ev._end).toBeNull()
  })
})

describe('listEvents', () => {
  it('reads the {events:[]} envelope and sorts by start', async () => {
    vi.stubGlobal('fetch', mockFetch(200, {
      events: [
        { uid: '2', title: 'Later', start: '2026-01-02T09:00:00Z' },
        { uid: '1', title: 'Earlier', start: '2026-01-01T09:00:00Z' },
      ],
    }))
    const evs = await listEvents(new Date('2026-01-01'), new Date('2026-01-31'))
    expect(evs.map((e) => e.title)).toEqual(['Earlier', 'Later'])
  })
  it('accepts a bare array response too', async () => {
    vi.stubGlobal('fetch', mockFetch(200, [{ uid: '1', title: 'A', start: '2026-01-01T09:00:00Z' }]))
    const evs = await listEvents(new Date(), new Date())
    expect(evs).toHaveLength(1)
  })
  it('throws on a non-ok response so the UI can show "unavailable"', async () => {
    vi.stubGlobal('fetch', mockFetch(503, 'mail service not configured'))
    await expect(listEvents(new Date(), new Date())).rejects.toThrow()
  })
})

describe('write path maps the editor form to lilmail /v1 body', () => {
  it('createEvent POSTs {summary,start,end,location,description,allDay}', async () => {
    const spy = mockFetch(201, { event: {} })
    vi.stubGlobal('fetch', spy)
    await createEvent({ title: 'Standup', start: '2026-01-02T09:00:00Z', end: '2026-01-02T09:30:00Z', location: 'HQ', notes: 'daily', allDay: false })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/calendar/events')
    if (!opts) throw new Error('expected call options')
    expect(opts.method).toBe('POST')
    if (typeof opts.body !== 'string') throw new Error('expected a JSON string body')
    const sent: unknown = JSON.parse(opts.body)
    expect(sent).toMatchObject({ summary: 'Standup', location: 'HQ', description: 'daily', allDay: false })
  })
  it('updateEvent PUTs to the id-scoped path', async () => {
    const spy = mockFetch(200, { event: {} })
    vi.stubGlobal('fetch', spy)
    await updateEvent('evt 1', { title: 'X', start: 's', end: 'e' })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/calendar/events/evt%201')
    if (!opts) throw new Error('expected call options')
    expect(opts.method).toBe('PUT')
  })
  it('deleteEvent DELETEs the id and tolerates an empty 204 body', async () => {
    const spy = mockFetch(204, '')
    vi.stubGlobal('fetch', spy)
    await expect(deleteEvent('evt1')).resolves.toBeNull()
    const [, opts] = spy.mock.calls[0]
    if (!opts) throw new Error('expected call options')
    expect(opts.method).toBe('DELETE')
  })
})
