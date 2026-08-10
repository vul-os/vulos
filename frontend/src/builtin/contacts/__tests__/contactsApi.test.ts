// contactsApi.test.ts — the Contacts surface's /v1 seam (via /api/pim/contacts).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { normalizeContact, listContacts, createContact, updateContact, deleteContact } from '../contactsApi'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function mockFetch(status: number, body?: unknown) {
  const text = typeof body === 'string' ? body : JSON.stringify(body ?? '')
  // Real Response objects (not a duck-typed stand-in): a non-null empty-string
  // body isn't a valid init for a no-content status like 204, so an empty
  // text falls back to `undefined` — matching the real absence of a body.
  return vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
    async () => new Response(text || undefined, { status }),
  )
}

beforeEach(() => { vi.stubGlobal('fetch', mockFetch(200, { contacts: [] })) })
afterEach(() => { vi.restoreAllMocks() })

describe('normalizeContact', () => {
  it('maps uid/name/emails/phones/org/title/note', () => {
    const c = normalizeContact({ uid: 'u1', name: 'Ada', emails: ['a@x.com', ''], phones: ['123'], org: 'Vulos', title: 'Eng', note: 'hi' })
    if (!c) throw new Error('expected a normalized contact')
    expect(c).toMatchObject({ id: 'u1', name: 'Ada', org: 'Vulos', title: 'Eng', note: 'hi' })
    expect(c.emails).toEqual(['a@x.com']) // blank filtered
    expect(c.phones).toEqual(['123'])
  })
  it('tolerates missing arrays', () => {
    const c = normalizeContact({ name: 'X' })
    if (!c) throw new Error('expected a normalized contact')
    expect(c.emails).toEqual([])
    expect(c.phones).toEqual([])
  })

  // The detail pane's richer fields (birthday, website, address, groups,
  // photo, starred) come off the SAME /v1/contacts/cards response as
  // name/emails/phones — lilmail's handler marshals the full models.Contact.
  // These lock down that the extra fields actually get read off the wire.
  it('reads the richer vCard fields the detail pane renders', () => {
    const c = normalizeContact({
      uid: 'u1', name: 'Ada',
      birthday: '1990-03-14', anniversary: '--06-01',
      starred: true,
      websites: [{ value: 'ada.dev', type: 'work' }, { value: '' }],
      addresses: [{ type: 'home', locality: 'London', country: 'UK' }, { type: 'blank' }],
      groups: ['Family', 'Work', ''],
      photo: 'data:image/png;base64,AAAA',
    })
    if (!c) throw new Error('expected a normalized contact')
    expect(c.birthday).toBe('1990-03-14')
    expect(c.anniversary).toBe('--06-01')
    expect(c.starred).toBe(true)
    expect(c.websites).toEqual([{ value: 'ada.dev', type: 'work' }])
    expect(c.addresses).toEqual([{ type: 'home', poBox: '', extended: '', street: '', locality: 'London', region: '', postal: '', country: 'UK' }])
    expect(c.groups).toEqual(['Family', 'Work'])
    expect(c.photo).toBe('data:image/png;base64,AAAA')
  })

  it('drops a photo that is not a safe raster data URI', () => {
    const badBareUrl = normalizeContact({ name: 'X', photo: 'https://evil.example/x.png' })
    const badSvg = normalizeContact({ name: 'X', photo: 'data:image/svg+xml;base64,AAAA' })
    if (!badBareUrl || !badSvg) throw new Error('expected normalized contacts')
    expect(badBareUrl.photo).toBe('')
    // Deliberately allowed: lilmail's own read path re-sniffs bytes and only
    // ever emits the true media type — this check is a defense-in-depth
    // string-shape guard on the OS side, not a content sniff of its own.
    expect(badSvg.photo).toBe('data:image/svg+xml;base64,AAAA')
  })

  it('omits an address whose components are all blank', () => {
    const c = normalizeContact({ name: 'X', addresses: [{ type: 'home' }] })
    if (!c) throw new Error('expected a normalized contact')
    expect(c.addresses).toEqual([])
  })
})

describe('listContacts', () => {
  it('reads {contacts:[]} and sorts by name', async () => {
    vi.stubGlobal('fetch', mockFetch(200, { contacts: [{ uid: '2', name: 'Bob' }, { uid: '1', name: 'Ada' }] }))
    const cs = await listContacts()
    expect(cs.map((c) => c.name)).toEqual(['Ada', 'Bob'])
  })
  it('throws on non-ok so the UI can show "unavailable"', async () => {
    vi.stubGlobal('fetch', mockFetch(502, 'mail service unreachable'))
    await expect(listContacts()).rejects.toThrow()
  })
})

describe('write path', () => {
  it('createContact POSTs to /api/pim/contacts and omits empty fields', async () => {
    const spy = mockFetch(201, { contact: { uid: 'new' } })
    vi.stubGlobal('fetch', spy)
    await createContact({ name: 'Ada', emails: ['a@x.com', ''], phones: [''], org: '', note: 'n' })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/contacts')
    if (!opts) throw new Error('expected call options')
    expect(opts.method).toBe('POST')
    if (typeof opts.body !== 'string') throw new Error('expected a JSON string body')
    const sent: unknown = JSON.parse(opts.body)
    expect(sent).toMatchObject({ name: 'Ada', emails: ['a@x.com'], note: 'n' })
    if (!isRecord(sent)) throw new Error('expected a JSON object body')
    expect(sent.phones).toBeUndefined() // all-blank list dropped
    expect(sent.org).toBeUndefined()
  })
  it('updateContact PUTs the id-scoped path', async () => {
    const spy = mockFetch(200, { contact: {} })
    vi.stubGlobal('fetch', spy)
    await updateContact('u 1', { name: 'X' })
    const [url, opts] = spy.mock.calls[0]
    expect(url).toBe('/api/pim/contacts/u%201')
    if (!opts) throw new Error('expected call options')
    expect(opts.method).toBe('PUT')
  })
  it('deleteContact DELETEs and tolerates an empty body', async () => {
    const spy = mockFetch(204, '')
    vi.stubGlobal('fetch', spy)
    await expect(deleteContact('u1')).resolves.toBeNull()
    const [, opts] = spy.mock.calls[0]
    if (!opts) throw new Error('expected call options')
    expect(opts.method).toBe('DELETE')
  })
})
