// calendarApi.test.js — the Calendar surface's /v1 seam (via /api/pim/calendar).
//
// Proves the read path is defensive (tolerates the {events:[]} envelope, a bare
// array, and both title/summary + start/dtStart spellings; drops junk events),
// and that the write path maps the editor form onto lilmail's create/update body.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { normalizeEvent, listEvents, createEvent, updateEvent, deleteEvent } from '../calendarApi'

function mockFetch(status, body) {
  return vi.fn().mockResolvedValue({
    ok: status < 300,
    status,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body ?? '')),
  })
}

beforeEach(() => { global.fetch = mockFetch(200, { events: [] }) })
afterEach(() => { vi.restoreAllMocks() })

describe('normalizeEvent', () => {
  it('tolerates title/summary and start/dtStart spellings', () => {
    expect(normalizeEvent({ uid: 'a', summary: 'Sync', dtStart: '2026-01-01T09:00:00Z' }).title).toBe('Sync')
    expect(normalizeEvent({ id: 'b', title: 'Call', start: '2026-01-01T10:00:00Z' }).id).toBe('b')
  })
  it('drops an event with neither title nor a parseable start', () => {
    expect(normalizeEvent({ uid: 'x', start: 'not-a-date' })).toBeNull()
    expect(normalizeEvent(null)).toBeNull()
  })
  it('parses dates into _start/_end without throwing on bad input', () => {
    const ev = normalizeEvent({ title: 'T', start: '2026-01-01T09:00:00Z', end: 'garbage' })
    expect(ev._start).toBeInstanceOf(Date)
    expect(ev._end).toBeNull()
  })
})

describe('listEvents', () => {
  it('reads the {events:[]} envelope and sorts by start', async () => {
    global.fetch = mockFetch(200, {
      events: [
        { uid: '2', title: 'Later', start: '2026-01-02T09:00:00Z' },
        { uid: '1', title: 'Earlier', start: '2026-01-01T09:00:00Z' },
      ],
    })
    const evs = await listEvents(new Date('2026-01-01'), new Date('2026-01-31'))
    expect(evs.map((e) => e.title)).toEqual(['Earlier', 'Later'])
  })
  it('accepts a bare array response too', async () => {
    global.fetch = mockFetch(200, [{ uid: '1', title: 'A', start: '2026-01-01T09:00:00Z' }])
    const evs = await listEvents(new Date(), new Date())
    expect(evs).toHaveLength(1)
  })
  it('throws on a non-ok response so the UI can show "unavailable"', async () => {
    global.fetch = mockFetch(503, 'mail service not configured')
    await expect(listEvents(new Date(), new Date())).rejects.toThrow()
  })
})

describe('write path maps the editor form to lilmail /v1 body', () => {
  it('createEvent POSTs {summary,start,end,location,description,allDay}', async () => {
    const spy = mockFetch(201, { event: {} })
    global.fetch = spy
    await createEvent({ title: 'Standup', start: '2026-01-02T09:00:00Z', end: '2026-01-02T09:30:00Z', location: 'HQ', notes: 'daily', allDay: false })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/calendar/events')
    expect(opts.method).toBe('POST')
    const sent = JSON.parse(opts.body)
    expect(sent).toMatchObject({ summary: 'Standup', location: 'HQ', description: 'daily', allDay: false })
  })
  it('updateEvent PUTs to the id-scoped path', async () => {
    const spy = mockFetch(200, { event: {} })
    global.fetch = spy
    await updateEvent('evt 1', { title: 'X', start: 's', end: 'e' })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/calendar/events/evt%201')
    expect(opts.method).toBe('PUT')
  })
  it('deleteEvent DELETEs the id and tolerates an empty 204 body', async () => {
    const spy = mockFetch(204, '')
    global.fetch = spy
    await expect(deleteEvent('evt1')).resolves.toBeNull()
    expect(spy.mock.calls[0][1].method).toBe('DELETE')
  })
})
